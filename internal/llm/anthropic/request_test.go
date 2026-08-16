package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

	// Adaptive, never budget_tokens: the field was removed from the API and is a
	// 400 on the models this runs against.
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
	body, _ := json.Marshal(params)
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
			"no content",
			llm.Message{Role: llm.RoleUser},
			"empty message",
		},
		{
			"only a dropped block",
			llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockThinking, Text: "x"}}},
			"empty message",
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
