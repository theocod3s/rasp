package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/theocod3s/rasp/internal/llm"
)

// TestRequestOnTheWire reads the bytes the adapter actually sent, rather than
// the params struct it built: the cache breakpoint is a field on a system block
// whose absence is invisible from the Go side and costs the whole prefix.
func TestRequestOnTheWire(t *testing.T) {
	client := replay(t, "text.sse")

	req := ask()
	req.System = []llm.SystemBlock{
		{Text: "You are rasp.", Cache: true},
		{Text: "Today is Tuesday."},
	}
	req.Thinking = llm.ThinkingConfig{Enabled: true}

	for range client.Stream(context.Background(), req) {
	}

	sent := client.sent(t)
	if sent["model"] != "claude-opus-5" {
		t.Errorf("model = %v", sent["model"])
	}
	if sent["max_tokens"] != float64(1024) {
		t.Errorf("max_tokens = %v", sent["max_tokens"])
	}

	system, _ := json.Marshal(sent["system"])
	var blocks []struct {
		Text         string          `json:"text"`
		CacheControl json.RawMessage `json:"cache_control"`
	}
	if err := json.Unmarshal(system, &blocks); err != nil {
		t.Fatalf("system blocks: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("sent %d system blocks, want 2", len(blocks))
	}
	if blocks[0].CacheControl == nil {
		t.Error("the block flagged Cache went out with no cache_control, so nothing after it is cached")
	}
	if blocks[1].CacheControl != nil {
		t.Error("an unflagged system block went out with a cache breakpoint")
	}

	// Adaptive, never budget_tokens: current models reject the budget shape, and a
	// caller who asks for one is refused rather than silently sent this instead.
	thinking, _ := json.Marshal(sent["thinking"])
	if got := string(thinking); !strings.Contains(got, `"adaptive"`) || strings.Contains(got, "budget_tokens") {
		t.Errorf("thinking = %s", got)
	}
}

func TestBuildParamsRejectsWhatItCannotSend(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block llm.Block
		want  string
	}{
		{"tool_use", llm.Block{Type: llm.BlockToolUse, ID: "toolu_01", Name: "read"}, "tool_use"},
		{"tool_result", llm.Block{Type: llm.BlockToolResult, ToolUseID: "toolu_01", Content: "ok"}, "tool_result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := ask()
			req.Messages = append(req.Messages, llm.Message{
				Role:    llm.RoleAssistant,
				Content: []llm.Block{tc.block},
			})

			_, err := buildParams(req)
			if err == nil {
				t.Fatal("no error; a block dropped from a request breaks the tool_use/tool_result pairing where nothing above can still see it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one naming %s", err, tc.want)
			}
		})
	}
}

// TestBuildParamsDropsThinking documents the one block that is dropped rather
// than refused. Replay is required only of a turn that went on to call a tool,
// and llm.Block has no field for the signature it would have to be replayed with.
func TestBuildParamsDropsThinking(t *testing.T) {
	req := ask()
	req.Messages = append(req.Messages,
		llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockThinking, Text: "internal"},
			{Type: llm.BlockText, Text: "visible"},
		}},
	)

	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	body, err := json.Marshal(params)
	if err != nil || len(body) == 0 {
		t.Fatalf("marshalling the params: %v; a nil body would pass every check below", err)
	}
	if strings.Contains(string(body), "internal") {
		t.Error("a thinking block went out without the signature Anthropic requires with it")
	}
	if !strings.Contains(string(body), "visible") {
		t.Error("dropping the thinking block took the text block with it")
	}
}

func TestBuildParamsRejectsEmptyAndUnknown(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  llm.Message
		want string
	}{
		{
			// A user message, so the skip that saves the assistant's does not apply.
			"no content",
			llm.Message{Role: llm.RoleUser},
			`a "user" message has nothing left to send`,
		},
		{
			"unknown role",
			llm.Message{Role: "system", Content: []llm.Block{{Type: llm.BlockText, Text: "x"}}},
			"unknown role",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := ask()
			req.Messages = []llm.Message{tc.msg}

			if _, err := buildParams(req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// TestStreamRefusesToSendBadRequest: buildParams fails before any HTTP call, and
// that failure has to leave through the same terminal EventError as every other.
// The server fails the test if it is reached, because a request that went out and
// came back 401 would produce the same single terminal error as one never sent.
func TestStreamRefusesToSendBadRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the adapter sent a request it should have refused to build")
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	req := ask()
	req.Messages = []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult}}}}

	client := New(Config{APIKey: "test", BaseURL: srv.URL})
	events, err := llm.CheckStream(client.Stream(context.Background(), req))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event types = %v, want a single terminal error", types(events))
	}
	if events[0].Type != llm.EventError || events[0].StopReason != llm.StopError {
		t.Errorf("terminal event = %s/%s", events[0].Type, events[0].StopReason)
	}
}

// TestBuildParamsRefusesTools covers the one field the adapter reads nowhere. It
// would otherwise vanish between the caller and the wire, and the turn would look
// successful with the request quietly missing half its meaning.
func TestBuildParamsRefusesTools(t *testing.T) {
	req := ask()
	req.Tools = []llm.ToolSpec{{Name: "read", Description: "read a file"}}

	_, err := buildParams(req)
	if err == nil {
		t.Fatal("no error; the model would answer in prose with the tools never offered, and nothing above could tell")
	}
	if !strings.Contains(err.Error(), "tools") {
		t.Errorf("error = %v, want one naming the tools", err)
	}
}

// TestBuildParamsRefusesUnsetMaxTokens: the zero value a caller gets by forgetting
// the field serializes as "max_tokens":0, which costs an authenticated round trip
// to be told about. Every other unsendable shape in this file is refused locally.
func TestBuildParamsRefusesUnsetMaxTokens(t *testing.T) {
	req := ask()
	req.MaxTokens = 0

	if _, err := buildParams(req); err == nil || !strings.Contains(err.Error(), "MaxTokens") {
		t.Errorf("error = %v, want one naming MaxTokens", err)
	}
}

// TestBuildParamsSkipsAThinkingOnlyTurn: a turn truncated while the model was
// still thinking commits an assistant message holding nothing else. Refusing it
// would refuse every later request built from the same transcript, so one
// truncated turn would end the session with no way out but editing the file.
func TestBuildParamsSkipsAThinkingOnlyTurn(t *testing.T) {
	req := ask()
	req.Messages = append(req.Messages,
		llm.Message{
			Role:       llm.RoleAssistant,
			Content:    []llm.Block{{Type: llm.BlockThinking, Text: "Still reasoning when the cap hit"}},
			StopReason: llm.StopMaxTokens,
		},
		llm.Message{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: "carry on"}}},
	)

	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v; the session is now unrecoverable without editing the file", err)
	}
	// The thinking-only turn is skipped, and the user turns either side of it are
	// combined rather than left adjacent.
	if len(params.Messages) != 1 {
		t.Fatalf("sent %d messages, want the thinking-only turn omitted and the user turns merged",
			len(params.Messages))
	}
	assertRolesAlternate(t, params)

	body, err := json.Marshal(params)
	if err != nil || len(body) == 0 {
		t.Fatalf("marshalling the params: %v; a nil body would pass every check below", err)
	}
	if strings.Contains(string(body), "Still reasoning") {
		t.Error("the thinking block went out without the signature Anthropic requires with it")
	}
	// Both surviving turns have to reach the wire: merging that dropped one would
	// have the model answer a question nobody asked.
	for _, want := range []string{"read auth.go", "carry on"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("merging lost %q: %s", want, body)
		}
	}
}

// TestNewSatisfiesProvider: Stream's signature is pinned incidentally by
// CheckStream, ID's by nothing, so a rename here would compile and stay green.
func TestNewSatisfiesProvider(t *testing.T) {
	var p llm.Provider = New(Config{APIKey: "test"})
	if p.ID() != ProviderID {
		t.Errorf("ID() = %q, want %q", p.ID(), ProviderID)
	}
}

// TestReplayingATurnCutOnABlockBoundary walks the whole round trip, because the
// hazard only exists across it: a stream cut between content_block_start and the
// first delta commits a text block with no text, Anthropic rejects one, and the
// block is in the transcript — so sending it would fail every later request in the
// session, not just the one that carries it.
func TestReplayingATurnCutOnABlockBoundary(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "empty_block.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}

	// The second block never got past its start frame. Projection is positional, so
	// it is committed as an empty block rather than dropped, and the send side is
	// what has to keep it off the wire.
	committed := *last(t, events).Partial
	if len(committed.Content) != 2 || committed.Content[1].Text != "" {
		t.Fatalf("committed content = %+v, want a written block and an empty one", committed.Content)
	}

	// Now build the next turn from that transcript, the way the loop would.
	next := ask()
	next.Messages = append(next.Messages, committed,
		llm.Message{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: "go on"}}})

	params, err := buildParams(next)
	if err != nil {
		t.Fatalf("buildParams: %v; every later request in this session would fail the same way", err)
	}
	assertSendsNoEmptyTextBlock(t, params, "One moment.")
}

// TestReplayingARefusal: a model can decline before producing anything, which
// arrives as a 200 with no blocks at all — contract.go exempts StopRefusal from
// its emptiness rule for exactly that reason, so the state is one the repo already
// says is reachable. The committed message is then unsendable, and refusing it
// would fail not just the next request but every request built from that
// transcript afterwards. StopAborted reaches the same place.
func TestReplayingARefusal(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "refusal.sse").Stream(context.Background(), ask()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}

	committed := *last(t, events).Partial
	if committed.StopReason != llm.StopRefusal || len(committed.Content) != 0 {
		t.Fatalf("committed = %s with %d blocks, want a refusal with none",
			committed.StopReason, len(committed.Content))
	}

	next := ask()
	next.Messages = append(next.Messages, committed,
		llm.Message{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: "why not?"}}})

	params, err := buildParams(next)
	if err != nil {
		t.Fatalf("buildParams: %v; the session is wedged from here on, with no way out but "+
			"editing the transcript by hand", err)
	}
	// The refusal is skipped, and the two user turns it sat between are combined
	// rather than sent back to back. Both have to survive that: dropping either
	// would have the model answer a question nobody asked.
	if got := len(params.Messages); got != 1 {
		t.Fatalf("sent %d messages, want the two user turns merged into one", got)
	}
	assertSendsNoEmptyTextBlock(t, params, "why not?")
	assertSendsNoEmptyTextBlock(t, params, "read auth.go")
	assertRolesAlternate(t, params)
}

// assertRolesAlternate fails on two adjacent messages with the same role. The
// skip above is what creates them, and the API's own error table and reference
// disagree about whether it rejects them or combines them — so this does not
// depend on finding out which.
func assertRolesAlternate(t *testing.T, params sdk.MessageNewParams) {
	t.Helper()
	if len(params.Messages) == 0 {
		t.Fatal("no messages, so nothing below was checked")
	}
	for i := 1; i < len(params.Messages); i++ {
		if params.Messages[i].Role == params.Messages[i-1].Role {
			t.Errorf("messages %d and %d are both %q", i-1, i, params.Messages[i].Role)
		}
	}
}

// TestSendingSkipsAnEmptyBlockAlreadyInATranscript: projection is positional, so a
// block that never filled is committed and stays committed — Load repairs pairing,
// not block contents. The send-side skip is what keeps such a transcript usable,
// and it is asserted against a message built by hand so the rule is pinned on its
// own, rather than through whatever a fixture happens to produce.
func TestSendingSkipsAnEmptyBlockAlreadyInATranscript(t *testing.T) {
	next := ask()
	next.Messages = append(next.Messages,
		llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockText, Text: "One moment."},
			{Type: llm.BlockText, Text: ""},
		}},
		llm.Message{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: "go on"}}})

	params, err := buildParams(next)
	if err != nil {
		t.Fatalf("buildParams: %v; every later request in this session would fail the same way", err)
	}
	assertSendsNoEmptyTextBlock(t, params, "One moment.")
}

// assertSendsNoEmptyTextBlock marshals what would go on the wire and fails on any
// text block with no text, and on the absence of want.
//
// Decoded rather than matched as a substring. The SDK picks the key order, so
// checking for a literal `{"text":"","type":"text"}` is an assertion that silently
// stops being able to fire the day that order changes — green for the same reason
// it was green before, while the regression it guards ships. The counts below are
// the other half: a decode shape that stops matching what the SDK emits finds
// nothing to examine, and finding nothing is not a pass.
func assertSendsNoEmptyTextBlock(t *testing.T, params sdk.MessageNewParams, want string) {
	t.Helper()

	body, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshalling the params: %v", err)
	}
	var decoded struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decoding the request body: %v", err)
	}
	if len(decoded.Messages) == 0 {
		t.Fatalf("the request carried no messages, so nothing below was checked: %s", body)
	}

	texts := 0
	for i, msg := range decoded.Messages {
		if len(msg.Content) == 0 {
			t.Errorf("message %d went out with no content blocks, which the API rejects", i)
		}
		for j, block := range msg.Content {
			// The SDK's block union marshals to a JSON null when it is left at its
			// zero value, which decodes back into a block with no type — and every
			// check below skips one of those.
			if block.Type == "" {
				t.Errorf("message %d block %d went out as a null rather than a block: %s", i, j, body)
				continue
			}
			if block.Type != "text" {
				continue
			}
			texts++
			if block.Text == "" {
				t.Errorf("message %d block %d is an empty text block, which the API rejects: %s", i, j, body)
			}
		}
	}
	if texts == 0 {
		t.Fatalf("no text blocks were examined; the shape decoded here no longer matches what the "+
			"SDK emits, so the check above cannot fail: %s", body)
	}
	if !strings.Contains(string(body), want) {
		t.Errorf("dropping the empty block took %q with it: %s", want, body)
	}
}

func TestBuildParamsRejectsEmptySystemBlock(t *testing.T) {
	req := ask()
	req.System = []llm.SystemBlock{{Text: "You are rasp."}, {Text: "", Cache: true}}

	if _, err := buildParams(req); err == nil || !strings.Contains(err.Error(), "system block 1") {
		t.Errorf("error = %v, want one naming the empty system block", err)
	}
}

// TestBuildParamsSkipIsAssistantOnly: the skip exists for a transcript state only
// an interrupted assistant turn produces. A user message is written by rasp, so an
// unsendable one is a bug here — and skipping it would leave the model answering
// the previous turn again, with the user's words still in the transcript.
func TestBuildParamsSkipIsAssistantOnly(t *testing.T) {
	req := ask()
	req.Messages = append(req.Messages, llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.Block{{Type: llm.BlockText, Text: ""}},
	})

	_, err := buildParams(req)
	if err == nil {
		t.Fatal("no error; the user's turn vanished from the request while staying in the transcript")
	}
	// Named, not merely non-nil: this request is well-formed apart from that one
	// message, so an error about anything else means the skip was applied and the
	// failure came from somewhere later.
	if !strings.Contains(err.Error(), `a "user" message has nothing left to send`) {
		t.Errorf("error = %v, want one naming the empty user message", err)
	}
}

// TestBuildParamsLeavesTheTranscriptAlone: an unsendable message is withheld
// from the request, never deleted. The caller keeps this slice and rebuilds a
// request from it every turn, so filtering it in place would take a refusal off
// the screen and out of the session file as well as off the wire.
func TestBuildParamsLeavesTheTranscriptAlone(t *testing.T) {
	req := ask()
	req.Messages = append(req.Messages,
		llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopRefusal},
		// Both kinds of block the send side leaves out, each in front of one it
		// keeps: filtering in place would slide the survivor into the gap.
		llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockThinking, Text: "reasoning"},
			{Type: llm.BlockText},
			{Type: llm.BlockText, Text: "One moment."},
		}},
		llm.Message{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: "go on"}}},
	)

	// A deep copy compared field by field, not the two encodings: Block.MarshalJSON
	// zeroes whatever a block's type does not own, so a comparison through JSON
	// cannot see a write to one of those fields at all. Input is cloned too, being
	// the one field a shallower copy would leave aliasing the original.
	before := slices.Clone(req.Messages)
	for i, msg := range req.Messages {
		before[i].Content = slices.Clone(msg.Content)
		for j, block := range msg.Content {
			before[i].Content[j].Input = bytes.Clone(block.Input)
		}
	}

	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	if !reflect.DeepEqual(before, req.Messages) {
		t.Errorf("building the request rewrote the transcript:\nbefore %+v\n after %+v", before, req.Messages)
	}
	// And the request it built: leaving the transcript alone while dropping the
	// survivor from the request would pass everything above.
	assertSendsNoEmptyTextBlock(t, params, "One moment.")
}

// TestBuildParamsRefusesAnEmptyMessageList is what the skip can reach on its own:
// a transcript of nothing but interrupted assistant turns leaves no messages, and
// the API refuses an empty list.
func TestBuildParamsRefusesAnEmptyMessageList(t *testing.T) {
	req := ask()
	req.Messages = []llm.Message{{
		Role:    llm.RoleAssistant,
		Content: []llm.Block{{Type: llm.BlockThinking, Text: "cut off here"}},
	}}

	if _, err := buildParams(req); err == nil || !strings.Contains(err.Error(), "no messages") {
		t.Errorf("error = %v, want one about there being no messages left", err)
	}
}

func TestBuildParamsRefusesUnsetModel(t *testing.T) {
	req := ask()
	req.Model = ""

	if _, err := buildParams(req); err == nil || !strings.Contains(err.Error(), "no model") {
		t.Errorf("error = %v, want one naming the missing model", err)
	}
}
