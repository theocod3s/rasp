package openaicompat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

	// No message_start: this wire has no equivalent, which is the case the contract
	// leaves that event optional for.
	want := []llm.EventType{llm.EventTextDelta, llm.EventTextDelta, llm.EventTextDelta, llm.EventDone}
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
	if msg.Role != llm.RoleAssistant || msg.Provider != testProvider || msg.Model != "openai/gpt-4o-mini" {
		t.Errorf("message identity = %s/%s/%s", msg.Role, msg.Provider, msg.Model)
	}
	if msg.StopReason != llm.StopEndTurn {
		t.Errorf("stop reason = %q, want %q", msg.StopReason, llm.StopEndTurn)
	}
}

// TestStreamUsageIsReported is the assertion CheckStream cannot make. Its
// monotonicity rule catches a count revised downward, but an adapter that never
// mapped usage at all climbs from zero and passes, because at that level it cannot
// be told from an endpoint reporting none (design §3.1a) — so the numbers this
// endpoint reports are named here.
//
// The subtraction is the part worth pinning: `prompt_tokens` counts the cached
// half too, where llm.Usage.Input excludes it, so an adapter assigning the wire's
// number straight across counts 800 tokens twice on every cached turn.
func TestStreamUsageIsReported(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "text.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	want := llm.Usage{Input: 400, Output: 42, CacheRead: 800}
	if got := last(t, events).Partial.Usage; got != want {
		t.Errorf("final usage = %+v, want %+v", got, want)
	}
}

// TestStreamWithoutUsage is the other dialect, and the reason CheckStream cannot
// require usage at all: an endpoint that ignores stream_options reports none, and
// the turn still has to be a valid stream (design §3.1a). Ollama with no
// `stream_options` sends exactly this.
func TestStreamWithoutUsage(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "text_no_usage.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	end := last(t, events)
	if end.Type != llm.EventDone || end.StopReason != llm.StopEndTurn {
		t.Fatalf("terminal event = %s/%s, want %s/%s", end.Type, end.StopReason, llm.EventDone, llm.StopEndTurn)
	}
	if got := end.Partial.Usage; got != (llm.Usage{}) {
		t.Errorf("usage = %+v, want the zero value; this stream carried no counts", got)
	}
	if got := end.Partial.Content[0].Text; got != "Hello!" {
		t.Errorf("accumulated text = %q; the reply is what the turn is for", got)
	}
}

// TestStreamPartialIsOneStableMessage pins the allocation site. Rendering reads
// Partial on every event, so a fresh message per event costs an allocation per
// token while still satisfying the contract's letter (design §3.1).
func TestStreamPartialIsOneStableMessage(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "tool_call.sse").Stream(context.Background(), askWithTools()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("the stream yielded %d events, so there is no second pointer to compare", len(events))
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

func TestStreamStopReasons(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		want    llm.StopReason
	}{
		{"truncated_tool_call.sse", llm.StopMaxTokens},
		{"refusal.sse", llm.StopRefusal},
		// `stop` alongside tool calls is the local-server dialect. Reading it as
		// end_turn presents a turn that stopped to call a tool as a finished answer.
		{"tool_call_stop_reason.sse", llm.StopToolUse},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			events, err := llm.CheckStream(replay(t, tc.fixture).Stream(context.Background(), askWithTools()))
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

// TestStreamRefusalIsCommitted: a refusal arrives in a field of its own on this
// wire, and it is prose the user has to be able to read. Dropped, the turn shows
// as a stop reason with nothing on screen to explain it.
func TestStreamRefusalIsCommitted(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "refusal.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	end := last(t, events)
	if len(end.Partial.Content) != 1 || end.Partial.Content[0].Type != llm.BlockText {
		t.Fatalf("content = %+v, want the refusal as one text block", end.Partial.Content)
	}
	if got := end.Partial.Content[0].Text; got != "I'm sorry, I can't help with that." {
		t.Errorf("refusal text = %q", got)
	}
}

// TestStreamUnsupportedFinishReason covers a reason the adapter has no mapping
// for. Defaulting to end_turn would hand the loop a stopped turn dressed as a
// finished answer, so it has to come back as a failure (decisions.md).
func TestStreamUnsupportedFinishReason(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "unknown_finish_reason.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	end := last(t, events)
	if end.Type != llm.EventError || end.StopReason != llm.StopError {
		t.Fatalf("terminal event = %s/%s, want %s/%s", end.Type, end.StopReason, llm.EventError, llm.StopError)
	}
	if end.Err == nil || !strings.Contains(end.Err.Error(), "function_call") {
		t.Errorf("error = %v, want one naming the reason", end.Err)
	}
}

// TestStreamCutShort is a connection that dies mid-message: no finish reason ever
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
	if end.Err == nil || !strings.Contains(end.Err.Error(), "finish reason") {
		t.Errorf("error = %v, want one about the missing finish reason", end.Err)
	}
	// What did arrive is still on the message, for the loop to commit.
	if got := end.Partial.Content[0].Text; got != "The connection drops after" {
		t.Errorf("text streamed before the cut = %q", got)
	}
}

// TestStreamMidStreamError: a gateway can answer 200 and then put a failure in the
// body, which is the shape that reaches neither the status code nor the finish
// reason. It has to leave through the same terminal error everything else does.
func TestStreamMidStreamError(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "mid_stream_error.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	end := last(t, events)
	if end.Type != llm.EventError || end.StopReason != llm.StopError {
		t.Fatalf("terminal event = %s/%s, want %s/%s", end.Type, end.StopReason, llm.EventError, llm.StopError)
	}
	if end.Err == nil || !strings.Contains(end.Err.Error(), "Provider returned error") {
		t.Errorf("error = %v, want one carrying what the gateway said", end.Err)
	}
	if !strings.Contains(end.Err.Error(), testProvider) {
		t.Errorf("error = %v, want one naming the endpoint; one adapter serves many", end.Err)
	}
}

// TestStreamRejectsAMismatchedChunk: the accumulator's one refusal is a chunk
// whose id names a different response. Ignoring it splices two completions into
// one message, which reads as a model that repeated itself.
func TestStreamRejectsAMismatchedChunk(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "mismatched_id.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	end := last(t, events)
	if end.Type != llm.EventError || end.StopReason != llm.StopError {
		t.Fatalf("terminal event = %s/%s, want %s/%s", end.Type, end.StopReason, llm.EventError, llm.StopError)
	}
	if end.Err == nil || !strings.Contains(end.Err.Error(), "gen-two") {
		t.Errorf("error = %v, want one naming the chunk that did not belong", end.Err)
	}
	if got := end.Partial.Content[0].Text; got != "Half of one reply" {
		t.Errorf("committed text = %q, want only what belonged to this response", got)
	}
}

// TestStreamRejectsASecondChoice: this adapter never asks for more than one
// completion, and it projects onto one message. A chunk for choice 1 has nowhere
// to go, and reading it as choice 0 would answer with a candidate nobody chose.
func TestStreamRejectsASecondChoice(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "second_choice.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	end := last(t, events)
	if end.Type != llm.EventError || end.StopReason != llm.StopError {
		t.Fatalf("terminal event = %s/%s, want %s/%s", end.Type, end.StopReason, llm.EventError, llm.StopError)
	}
	if end.Err == nil || !strings.Contains(end.Err.Error(), "choice 1") {
		t.Errorf("error = %v, want one naming the choice", end.Err)
	}
}

// TestStreamRequestFailure covers the half of stream.Err() that never reaches the
// decoder: a non-2xx response surfaces in exactly the place a mid-stream failure
// does, which is the property that keeps the adapter to one error path.
func TestStreamRequestFailure(t *testing.T) {
	client := respond(t, 401, `{"error":{"message":"No auth credentials found","code":401}}`)

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

// TestStreamCancelled: a cancelled turn is an error to the code that was streaming
// and a completion to design §4's termination table, which is why it reports
// StopAborted rather than StopError.
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

	client := New(Config{ProviderID: testProvider, APIKey: "test-key", BaseURL: srv.URL})
	var seen int
	for ev := range client.Stream(context.Background(), ask()) {
		seen++
		if ev.Type == llm.EventTextDelta {
			break
		}
	}
	if seen != 1 {
		t.Errorf("consumer saw %d events before breaking, want 1", seen)
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

// TestStreamNeedsABaseURL: this is the field that makes one adapter serve many
// endpoints, and the SDK's default for it is api.openai.com. A provider called
// "ollama" whose base_url never made it through configuration would otherwise send
// the conversation to OpenAI and read as working.
func TestStreamNeedsABaseURL(t *testing.T) {
	noAmbientCredentials(t)

	events, err := llm.CheckStream(New(Config{ProviderID: "ollama"}).Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event types = %v, want a single terminal error before any request", types(events))
	}
	if end := events[0]; end.Err == nil || !strings.Contains(end.Err.Error(), "base_url") {
		t.Errorf("error = %v, want one naming the missing setting", end.Err)
	}
}

// TestNoAPIKeyWithholdsAnAmbientOpenAIKey: the SDK resolves OPENAI_API_KEY on its
// own, and this adapter points at somebody else's server by definition — so
// leaving that chain alone would hand an OpenAI credential to OpenRouter, or to
// whatever is listening on a LAN address.
func TestNoAPIKeyWithholdsAnAmbientOpenAIKey(t *testing.T) {
	noAmbientCredentials(t)
	t.Setenv("OPENAI_API_KEY", "sk-ambient")

	client := replayAs(t, "text.sse", Config{ProviderID: "ollama"})
	for range client.Stream(context.Background(), ask()) {
	}

	headers := client.maybeHeaders()
	if headers == nil {
		t.Fatal("the adapter sent no request, so the credential in the environment was never reached")
	}
	if got := headers.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q; an OpenAI key went to an endpoint that is not OpenAI", got)
	}
}

// TestConfiguredKeyIsSent is the other half, and it is asserted because a client
// that sent no credential at all would satisfy the test above for the wrong reason.
func TestConfiguredKeyIsSent(t *testing.T) {
	noAmbientCredentials(t)
	t.Setenv("OPENAI_API_KEY", "sk-ambient")

	client := replayAs(t, "text.sse", Config{ProviderID: testProvider, APIKey: "sk-configured"})
	for range client.Stream(context.Background(), ask()) {
	}

	headers := client.maybeHeaders()
	if headers == nil {
		t.Fatal("the adapter sent no request, so neither credential was ever put on one")
	}
	if got := headers.Get("Authorization"); got != "Bearer sk-configured" {
		t.Errorf("Authorization = %q, want the configured key rather than the one in the environment", got)
	}
}

// TestConfiguredBaseURLBeatsTheEnvironment: OPENAI_BASE_URL is read by the SDK
// before any option is applied, and a session recorded against "ollama" that
// silently went somewhere else is a failure with nothing on screen to show it.
func TestConfiguredBaseURLBeatsTheEnvironment(t *testing.T) {
	noAmbientCredentials(t)
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1/v1")

	client := replay(t, "text.sse")
	events, err := llm.CheckStream(client.Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	if end := last(t, events); end.Type != llm.EventDone {
		t.Fatalf("terminal event = %s (%v), want the configured base URL to have been used", end.Type, end.Err)
	}
}
