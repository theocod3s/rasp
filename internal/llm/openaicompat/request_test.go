package openaicompat

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/llm"
)

// TestRequestAsksForUsage is the request half of what design §11 rests on. This
// wire reports no usage at all unless stream_options.include_usage is set, and the
// symptom of leaving it out is not an error — it is a session that estimates its
// own context from zero and compacts at the wrong point.
func TestRequestAsksForUsage(t *testing.T) {
	params, err := buildParams(ask())
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	body := requestBody(t, params)
	if !strings.Contains(body, `"stream_options":{"include_usage":true}`) {
		t.Errorf("request = %s, want stream_options asking for the counts", body)
	}
}

// TestRequestCapsTheReplyWithMaxTokens pins the deprecated field on purpose.
// `max_completion_tokens` is what OpenAI wants now, and Ollama accepts it and
// ignores it — so sending the current field caps nothing there and says nothing
// about it, where sending this one fails loudly on the models that refuse it.
func TestRequestCapsTheReplyWithMaxTokens(t *testing.T) {
	req := ask()
	req.MaxTokens = 4096

	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	body := requestBody(t, params)
	if !strings.Contains(body, `"max_tokens":4096`) {
		t.Errorf("request = %s, want the reply capped", body)
	}
}

func TestBuildParamsRefusesARequestTheAPIWouldReject(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*llm.Request)
		wantErr string
	}{
		{"no model", func(r *llm.Request) { r.Model = "" }, "no model"},
		{"no reply cap", func(r *llm.Request) { r.MaxTokens = 0 }, "MaxTokens is 0"},
		{"no messages", func(r *llm.Request) { r.Messages = nil }, "no messages left to send"},
		{
			"an empty system block",
			func(r *llm.Request) { r.System = []llm.SystemBlock{{Text: "be brief"}, {}} },
			"system block 1 has no text",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := ask()
			tc.mutate(&req)

			_, err := buildParams(req)
			if err == nil {
				t.Fatal("no error; the request would cost an authenticated round trip to be refused")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want one mentioning %q", err, tc.wantErr)
			}
		})
	}
}

// TestRequestSendsTheSystemPromptInOrder: the ordering in Request.System is where
// design §11 puts its cache breakpoints, and this wire has none to place — an
// OpenAI-compatible endpoint caches its prefix without being asked. What survives
// the translation is the order, which is the half that still does work here.
func TestRequestSendsTheSystemPromptInOrder(t *testing.T) {
	req := ask()
	req.System = []llm.SystemBlock{
		{Text: "You are rasp.", Cache: true},
		{Text: "The mode is manual."},
	}

	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	wire := decodeMessages(t, params)
	if got := roles(wire); !slices.Equal(got, []string{"system", "user"}) {
		t.Fatalf("roles on the wire = %v, want the system prompt first", got)
	}
	if got := wire[0].Content; got != "You are rasp.\n\nThe mode is manual." {
		t.Errorf("system prompt = %q", got)
	}
}

// TestBuildParamsSkipsAnEmptyAssistantTurn: four routes leave an assistant message
// with nothing sendable in it, and the message is on disk — so refusing it would
// fail every later request in that session, not only the next (decisions.md). The
// user turns either side of it are not skipped.
func TestBuildParamsSkipsAnEmptyAssistantTurn(t *testing.T) {
	req := ask()
	req.Messages = append(req.Messages,
		// A thinking-only turn: the block is dropped on its type, which is what makes
		// asking CheckSendable about the raw message the wrong question.
		llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockThinking, Text: "hmm"}}},
		llm.Message{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: "go on"}}},
	)

	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v; every later request in this session would fail the same way", err)
	}
	wire := decodeMessages(t, params)
	if got := roles(wire); !slices.Equal(got, []string{"user", "user"}) {
		t.Fatalf("roles on the wire = %v, want both user turns and no empty assistant one", got)
	}
	// Named on both, and on the count: with only "go on" asserted, a skip that took
	// the turn before it as well would read as a pass.
	if wire[0].Content != "read auth.go" || wire[1].Content != "go on" {
		t.Errorf("messages on the wire = %q / %q", wire[0].Content, wire[1].Content)
	}
}

// TestBuildParamsRefusesAnEmptyUserTurn is the other half of the same rule: rasp
// writes user messages, so an unsendable one is a bug here rather than a state the
// model left behind, and skipping it would leave the model answering the previous
// turn twice.
func TestBuildParamsRefusesAnEmptyUserTurn(t *testing.T) {
	req := ask()
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleUser})

	if _, err := buildParams(req); err == nil {
		t.Fatal("no error; an empty user turn went out or was silently dropped")
	}
}

func TestBuildParamsRefusesABlockItCannotSend(t *testing.T) {
	req := ask()
	req.Messages = append(req.Messages, llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.Block{{Type: llm.BlockType("some_future_block"), Text: "?"}},
	})

	_, err := buildParams(req)
	if err == nil {
		t.Fatal("no error; the block went out as something it is not, or the turn went out without it")
	}
	if !strings.Contains(err.Error(), "some_future_block") {
		t.Errorf("error = %v, want one naming the block type", err)
	}
}

// TestBuildParamsSendsAnEmptyToolResult: a tool that succeeds and prints nothing
// is ordinary, and the pairing design §4 invariant 1 rests on needs the message
// there whether or not it carries text.
func TestBuildParamsSendsAnEmptyToolResult(t *testing.T) {
	req := ask()
	req.Messages = append(req.Messages,
		llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockToolUse, ID: "call_quiet", Name: "bash", Input: json.RawMessage(`{"command":"true"}`)},
		}},
		llm.Message{Role: llm.RoleUser, Content: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: "call_quiet"},
		}},
	)

	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	wire := decodeMessages(t, params)
	if got := roles(wire); !slices.Equal(got, []string{"user", "assistant", "tool"}) {
		t.Fatalf("roles on the wire = %v", got)
	}
	if wire[2].ToolCallID != "call_quiet" || wire[2].Content != "" {
		t.Errorf("tool message = %+v", wire[2])
	}
}
