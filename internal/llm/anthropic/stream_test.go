package anthropic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/llm"
)

func TestStreamText(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "text.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}

	want := []llm.EventType{
		llm.EventMessageStart,
		llm.EventTextDelta, llm.EventTextDelta, llm.EventTextDelta,
		llm.EventDone,
	}
	if got := types(events); !slices.Equal(got, want) {
		t.Errorf("event types = %v, want %v", got, want)
	}

	// Deltas carry only what arrived; Partial carries the whole message. The
	// distinction is the contract's, so both halves are checked.
	var deltas []string
	for _, ev := range events {
		if ev.Type == llm.EventTextDelta {
			deltas = append(deltas, ev.Delta)
		}
	}
	if got := strings.Join(deltas, "|"); got != "I'll| read| it." {
		t.Errorf("deltas = %q", got)
	}

	msg := last(t, events).Partial
	if len(msg.Content) != 1 || msg.Content[0].Type != llm.BlockText {
		t.Fatalf("final content = %+v, want one text block", msg.Content)
	}
	if got := msg.Content[0].Text; got != "I'll read it." {
		t.Errorf("accumulated text = %q, want %q", got, "I'll read it.")
	}
	if msg.Role != llm.RoleAssistant || msg.Provider != ProviderID || msg.Model != "claude-opus-5" {
		t.Errorf("message identity = %s/%s/%s", msg.Role, msg.Provider, msg.Model)
	}
	if msg.StopReason != llm.StopEndTurn {
		t.Errorf("stop reason = %q, want %q", msg.StopReason, llm.StopEndTurn)
	}
}

// TestStreamUsageIsMerged is the assertion CheckStream cannot make. Its
// monotonicity rule catches a count revised downward, but an adapter that never
// read message_start climbs from zero and passes (design §3.1a) — so the numbers
// the fixture reports are named here. message_delta carries output_tokens alone,
// which is what makes assigning from it lose the other three.
func TestStreamUsageIsMerged(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "text.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}

	want := llm.Usage{Input: 1200, Output: 42, CacheRead: 800, CacheWrite: 64}
	if got := last(t, events).Partial.Usage; got != want {
		t.Errorf("final usage = %+v, want %+v", got, want)
	}

	// And the count was there from the first event, not filled in at the end.
	// Sampling has to happen DURING iteration: Partial is one pointer, so reading
	// events[0].Partial afterwards shows the final message and asserts nothing.
	// Context estimation reads Usage off whatever a turn left behind, including one
	// that broke off mid-stream.
	var first llm.Usage
	seen := 0
	for ev := range replay(t, "text.sse").Stream(context.Background(), ask()) {
		if seen == 0 {
			first = ev.Partial.Usage
		}
		seen++
	}
	if first.Input != 1200 || first.CacheRead != 800 {
		t.Errorf("usage on the first event = %+v, want the message_start counts already in place", first)
	}
}

// TestStreamPartialIsOneStableMessage pins the allocation site. Rendering reads
// Partial on every event, so a fresh message per event costs an allocation per
// token while still satisfying the contract's letter (design §3.1).
func TestStreamPartialIsOneStableMessage(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "thinking.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	for i, ev := range events {
		if ev.Partial == nil {
			t.Fatalf("event %d (%s) carries no Partial", i, ev.Type)
		}
		if ev.Partial != events[0].Partial {
			t.Fatalf("event %d (%s) carries a different *Message than event 0", i, ev.Type)
		}
	}
}

func TestStreamThinking(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "thinking.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}

	want := []llm.EventType{
		llm.EventMessageStart,
		llm.EventThinkingDelta, llm.EventThinkingDelta,
		llm.EventTextDelta,
		llm.EventDone,
	}
	if got := types(events); !slices.Equal(got, want) {
		t.Errorf("event types = %v, want %v", got, want)
	}

	msg := last(t, events).Partial
	if len(msg.Content) != 2 {
		t.Fatalf("final content = %+v, want a thinking block and a text block", msg.Content)
	}
	if msg.Content[0].Type != llm.BlockThinking || msg.Content[0].Text != "Two files to check." {
		t.Errorf("thinking block = %+v", msg.Content[0])
	}
	if msg.Content[1].Type != llm.BlockText || msg.Content[1].Text != "Both are fine." {
		t.Errorf("text block = %+v", msg.Content[1])
	}
}

func TestStreamStopReasons(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		want    llm.StopReason
	}{
		{"max_tokens.sse", llm.StopMaxTokens},
		{"refusal.sse", llm.StopRefusal},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			events, err := llm.CheckStream(replay(t, tc.fixture).Stream(context.Background(), ask()))
			if err != nil {
				t.Fatalf("CheckStream: %v", err)
			}
			end := last(t, events)
			if end.Type != llm.EventDone {
				t.Fatalf("terminal event = %s, want %s", end.Type, llm.EventDone)
			}
			if end.StopReason != tc.want {
				t.Errorf("stop reason = %q, want %q", end.StopReason, tc.want)
			}
			if end.Partial.StopReason != tc.want {
				t.Errorf("message stop reason = %q, want %q", end.Partial.StopReason, tc.want)
			}
		})
	}
}

// TestStreamUnsupportedStopReason covers a reason the adapter has no mapping
// for. Defaulting to end_turn would hand the loop a stopped turn dressed as a
// finished answer, so it has to come back as a failure.
func TestStreamUnsupportedStopReason(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "pause_turn.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	end := last(t, events)
	if end.Type != llm.EventError || end.StopReason != llm.StopError {
		t.Fatalf("terminal event = %s/%s, want %s/%s", end.Type, end.StopReason, llm.EventError, llm.StopError)
	}
	if end.Err == nil || !strings.Contains(end.Err.Error(), "pause_turn") {
		t.Errorf("error = %v, want one naming pause_turn", end.Err)
	}
}

// TestStreamCutShort is a connection that dies mid-message: no stop reason ever
// arrives, and the text streamed so far must not be presented as complete.
func TestStreamCutShort(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "cut_short.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	end := last(t, events)
	if end.Type != llm.EventError || end.StopReason != llm.StopError {
		t.Fatalf("terminal event = %s/%s, want %s/%s", end.Type, end.StopReason, llm.EventError, llm.StopError)
	}
	if end.Err == nil || !strings.Contains(end.Err.Error(), "stop reason") {
		t.Errorf("error = %v, want one about the missing stop reason", end.Err)
	}
	// What did arrive is still on the message, for the loop to commit.
	if got := end.Partial.Content[0].Text; got != "The connection drops after" {
		t.Errorf("text streamed before the cut = %q", got)
	}
}

// TestStreamRequestFailure covers the half of stream.Err() that never reaches the
// decoder: a non-2xx response surfaces in exactly the place a mid-stream failure
// does, which is the property that keeps the adapter to one error path.
func TestStreamRequestFailure(t *testing.T) {
	client := respond(t, 401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)

	events, err := llm.CheckStream(client.Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event types = %v, want a single terminal error", types(events))
	}
	end := events[0]
	if end.Type != llm.EventError || end.StopReason != llm.StopError {
		t.Fatalf("terminal event = %s/%s", end.Type, end.StopReason)
	}
	if end.Err == nil || !strings.Contains(end.Err.Error(), "401") {
		t.Errorf("error = %v, want one naming the status", end.Err)
	}
}

// TestStreamCancelled: a cancelled turn is an error to the code that was
// streaming and a completion to design §4's termination table, which is why it
// reports StopAborted rather than StopError.
func TestStreamCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	events, err := llm.CheckStream(replay(t, "text.sse").Stream(ctx, ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	end := last(t, events)
	if end.Type != llm.EventError || end.StopReason != llm.StopAborted {
		t.Fatalf("terminal event = %s/%s, want %s/%s", end.Type, end.StopReason, llm.EventError, llm.StopAborted)
	}
	if !errors.Is(end.Err, context.Canceled) {
		t.Errorf("error = %v, want one wrapping context.Canceled", end.Err)
	}
}

// TestStreamConsumerStopsEarly: yield returning false means the consumer has gone,
// and the adapter has to unwind and close the response body.
//
// The server holds the response open and watches for the disconnect, because
// goleak does not see this: with a fully buffered fixture the handler returns on
// its own and the test server reaps the connection at cleanup, so deleting the
// adapter's Close leaves every check green.
func TestStreamConsumerStopsEarly(t *testing.T) {
	body := fixtureBytes(t, "text.sse")
	disconnected := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(disconnected)
		case <-time.After(3 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)

	client := New(Config{APIKey: "test-key", BaseURL: srv.URL})
	var seen int
	for ev := range client.Stream(context.Background(), ask()) {
		seen++
		if ev.Type == llm.EventTextDelta {
			break
		}
	}
	if seen != 2 {
		t.Errorf("consumer saw %d events before breaking, want 2", seen)
	}
	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		t.Error("the server never saw the client go; the response body was left open")
	}
}

// TestStreamDoesNotRetry counts requests rather than timing the turn: a retry the
// adapter was not supposed to make is otherwise invisible. Why it must not is in New.
func TestStreamDoesNotRetry(t *testing.T) {
	client, requests := refuse(t)

	events, err := llm.CheckStream(client.Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	if got := last(t, events); got.Type != llm.EventError {
		t.Fatalf("terminal event = %s, want %s", got.Type, llm.EventError)
	}
	if n := requests(); n != 1 {
		t.Errorf("the adapter made %d requests for one turn, want 1: retrying here is llm/retry's job", n)
	}
}

// TestStreamDeadlineIsNotAnAbort pins the half of errorEvent's classification that
// looks like an oversight — a deadline is a context error and still not an abort.
func TestStreamDeadlineIsNotAnAbort(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	events, err := llm.CheckStream(replay(t, "text.sse").Stream(ctx, ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	end := last(t, events)
	if end.Type != llm.EventError || end.StopReason != llm.StopError {
		t.Fatalf("terminal event = %s/%s, want %s/%s", end.Type, end.StopReason, llm.EventError, llm.StopError)
	}
	if !errors.Is(end.Err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want one wrapping context.DeadlineExceeded", end.Err)
	}
}

// TestNoAPIKeyLeavesCredentialResolutionAlone needs a credential in the
// environment: with none, the SDK refuses either way and the guard in New looks
// like it changes nothing.
func TestNoAPIKeyLeavesCredentialResolutionAlone(t *testing.T) {
	noAmbientCredentials(t)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "ambient-token")

	client := replayAs(t, "text.sse", Config{})
	for range client.Stream(context.Background(), ask()) {
	}

	headers := client.maybeHeaders()
	if headers == nil {
		t.Fatal("the adapter sent no request, so the credential in the environment was never reached")
	}
	if _, present := headers["X-Api-Key"]; present {
		t.Error("an empty X-Api-Key went out; with no key configured the header belongs off the request")
	}
	if got := headers.Get("Authorization"); got != "Bearer ambient-token" {
		t.Errorf("Authorization = %q, want the environment's credential to have been used", got)
	}
}

// TestConfiguredKeyClearsAnAmbientBearer: the two credentials are resolved by
// different mechanisms and neither knows about the other. NewClient turns
// ANTHROPIC_AUTH_TOKEN into an Authorization header, WithAPIKey adds X-Api-Key
// beside it, and the server rejects a request holding both — so a gateway user who
// also configures a key would fail every turn, not just an unusual one.
func TestConfiguredKeyClearsAnAmbientBearer(t *testing.T) {
	noAmbientCredentials(t)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "ambient-token")

	client := replayAs(t, "text.sse", Config{APIKey: "sk-configured"})
	for range client.Stream(context.Background(), ask()) {
	}

	headers := client.maybeHeaders()
	if headers == nil {
		t.Fatal("the adapter sent no request, so neither credential was ever put on one")
	}
	// Asserted first: without it a client that sent no credential at all would
	// satisfy the check below and read as a pass.
	if got := headers.Get("X-Api-Key"); got != "sk-configured" {
		t.Fatalf("X-Api-Key = %q, want the configured key", got)
	}
	if got := headers.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q; it went out alongside the configured key, and the API "+
			"rejects a request carrying both", got)
	}
}

// TestStreamSurvivesInterleavedDeltas is the regression test for the one bug this
// package has shipped. The SDK documents that deltas interleave across open blocks
// and address them by index, so a later block can receive its text first. An
// earlier version skipped blocks that were still empty, which handed that later
// block the earlier one's index and rewrote it when the earlier one finally filled.
func TestStreamSurvivesInterleavedDeltas(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "interleaved_deltas.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v; a block that filled late took an index already drawn", err)
	}
	end := last(t, events)
	if len(end.Partial.Content) != 2 {
		t.Fatalf("content = %+v, want both blocks", end.Partial.Content)
	}
	// Positional: block i is at index i, whatever order the deltas arrived in.
	if end.Partial.Content[0].Text != "first" || end.Partial.Content[1].Text != "second" {
		t.Errorf("content = %q/%q, want the wire's own block order",
			end.Partial.Content[0].Text, end.Partial.Content[1].Text)
	}
}

// TestStreamFinishedTurnCarriesAnEmptyBlock: a turn that ends normally can carry a
// block that opened and closed without a delta, and this adapter commits it where
// it arrived — dropping it during the stream is the interleaving regression above,
// and dropping it at the terminal makes a block vanish instead. Keeping it off the
// wire is the send side's job, which is why the round trip is asserted here too.
func TestStreamFinishedTurnCarriesAnEmptyBlock(t *testing.T) {
	cases := map[string]struct {
		fixture string
		want    []llm.Block
	}{
		"a text block": {"finished_empty_block.sse", []llm.Block{
			{Type: llm.BlockText, Text: "Done."},
			{Type: llm.BlockText},
		}},
		// Opened with a signature and nothing else, so it is the FIRST block that
		// never filled: the empty one is not always the trailing one.
		"a thinking block": {"finished_empty_thinking.sse", []llm.Block{
			{Type: llm.BlockThinking},
			{Type: llm.BlockText, Text: "Done."},
		}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			events, err := llm.CheckStream(replay(t, tc.fixture).Stream(context.Background(), ask()))
			if err != nil {
				t.Fatalf("CheckStream: %v", err)
			}
			// The rule under test applies to a turn the model finished, and every
			// other assertion here holds just as well for a stream that broke off —
			// so the stop reason is checked rather than assumed.
			end := last(t, events)
			if end.Type != llm.EventDone || end.StopReason != llm.StopEndTurn {
				t.Fatalf("terminal event = %s/%s, want %s/%s", end.Type, end.StopReason,
					llm.EventDone, llm.StopEndTurn)
			}
			committed := *end.Partial
			if !reflect.DeepEqual(committed.Content, tc.want) {
				t.Fatalf("committed content = %+v, want %+v", committed.Content, tc.want)
			}

			next := ask()
			next.Messages = append(next.Messages, committed,
				llm.Message{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: "go on"}}})
			params, err := buildParams(next)
			if err != nil {
				t.Fatalf("buildParams: %v; every later request in this session would fail the same way", err)
			}
			assertSendsNoEmptyTextBlock(t, params, "Done.")
		})
	}
}

// TestStreamContextWindowExceeded: §12 wants this one fatal with a fix hint, and
// the retry classifier is a pure function over the message — so it has to be
// distinguishable from a stop reason nobody has taught the adapter yet.
func TestStreamContextWindowExceeded(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "context_exceeded.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	end := last(t, events)
	if end.Type != llm.EventError || end.StopReason != llm.StopError {
		t.Fatalf("terminal event = %s/%s", end.Type, end.StopReason)
	}
	if end.Err == nil || !strings.Contains(end.Err.Error(), "context window") {
		t.Errorf("error = %v, want one naming the context window rather than an unsupported reason", end.Err)
	}
	if strings.Contains(end.Err.Error(), "unsupported") {
		t.Error("the context-window overflow arrived as a generic unsupported stop reason")
	}
}

// TestStreamUnknownBlock: Anthropic ships new content block types regularly, and
// dropping one silently is the quietest failure the receive side could have — the
// user reads an answer with a hole in it, and a turn whose only block was unknown
// commits with no content at all, which every provider refuses on replay.
func TestStreamUnknownBlock(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "unknown_block.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	end := last(t, events)
	if end.Type != llm.EventError || end.StopReason != llm.StopError {
		t.Fatalf("terminal event = %s/%s, want %s/%s", end.Type, end.StopReason, llm.EventError, llm.StopError)
	}
	if end.Err == nil || !strings.Contains(end.Err.Error(), "some_future_block") {
		t.Errorf("error = %v, want one naming the block type", end.Err)
	}
}
