package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/llm"
)

// The live tests run only when liveVar is set, because they cost money and need a
// server on this machine. What they add over the fixtures is the half a recording
// cannot: that these endpoints still send what the fixtures say they send.
//
// Set, they FAIL rather than skip when a prerequisite is missing. A live check
// that quietly skips is the shape AGENTS.md names — the absence of a signal read
// as a pass — and it would report green having talked to nothing.
const (
	liveVar    = "RASP_LIVE"
	keyVar     = "OPENROUTER_API_KEY"
	liveModel  = "openai/gpt-4o-mini"
	ollamaURL  = "http://localhost:11434/v1"
	ollamaHost = "localhost:11434"
	localModel = "qwen2.5-coder:1.5b"
)

// TestLiveOpenRouter is the paid endpoint, and the one that reports usage in full.
// Design §3.1a cannot require usage of any adapter, so the assertion belongs here,
// against an endpoint known to report it: Message.Usage is what context estimation
// trusts (design §11), and an adapter that never mapped it climbs from zero and
// looks exactly like an endpoint that reports none.
func TestLiveOpenRouter(t *testing.T) {
	key := liveCredential(t, keyVar)

	client := New(Config{ProviderID: "openrouter", APIKey: key, BaseURL: "https://openrouter.ai/api/v1"})
	req := askWithTools()
	req.Model = liveModel
	req.MaxTokens = 200
	req.Messages = []llm.Message{{
		Role:    llm.RoleUser,
		Content: []llm.Block{{Type: llm.BlockText, Text: "Read auth.go and main.go."}},
	}}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	events, err := llm.CheckStream(client.Stream(ctx, req))
	transcribe(t, events)
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}

	end := last(t, events)
	if end.Type != llm.EventDone {
		t.Fatalf("terminal event = %s: %v", end.Type, end.Err)
	}
	if end.StopReason != llm.StopToolUse {
		t.Fatalf("stop reason = %q, want the model to have stopped to call a tool", end.StopReason)
	}
	if got := announced(events); len(got) == 0 {
		t.Fatal("the turn announced no tool calls, so the projection was never exercised")
	}
	for _, ev := range events {
		if ev.Type != llm.EventToolCall {
			continue
		}
		var args map[string]any
		if err := json.Unmarshal(ev.ToolCall.Input, &args); err != nil {
			t.Errorf("announced arguments %s do not parse: %v", ev.ToolCall.Input, err)
		}
		if args["path"] == nil {
			t.Errorf("announced arguments %s carry no path; the fragments were reassembled wrongly", ev.ToolCall.Input)
		}
	}

	usage := end.Partial.Usage
	if usage.Input <= 0 || usage.Output <= 0 {
		t.Errorf("usage = %+v; this endpoint reports counts when stream_options.include_usage is set, "+
			"and Message.Usage is what context estimation trusts", usage)
	}

	// The send side, which no recorded response can check: the block model carries a
	// turn's calls and their results in two messages, this API wants an assistant
	// message with tool_calls followed by one `tool` message each, and getting that
	// wrong is a 400 on this request and on every one built from the transcript
	// afterwards (design §4 invariant 1).
	next := req
	next.Messages = append(next.Messages, *end.Partial)
	answers := llm.Message{Role: llm.RoleUser}
	for _, ev := range events {
		if ev.Type == llm.EventToolCall {
			answers.Content = append(answers.Content, llm.Block{
				Type:      llm.BlockToolResult,
				ToolUseID: ev.ToolCall.ID,
				Content:   "package main",
			})
		}
	}
	next.Messages = append(next.Messages, answers)

	replayed, err := llm.CheckStream(client.Stream(ctx, next))
	transcribe(t, replayed)
	if err != nil {
		t.Fatalf("replaying the tool turn: %v", err)
	}
	if got := last(t, replayed); got.Type != llm.EventDone {
		t.Fatalf("replaying the tool turn ended %s: %v", got.Type, got.Err)
	}
}

// TestLiveOllama is the local endpoint, and protocol verification rather than
// intelligence: a 1.5B model is asked for prose, not for a tool call.
//
// Its usage is asserted because it was expected to report none and does. Ollama
// honours stream_options.include_usage — which makes the no-usage dialect a shape
// this suite meets only in text_no_usage.sse, recorded from the same server with
// the option left out.
func TestLiveOllama(t *testing.T) {
	liveOnly(t)
	if conn, err := net.DialTimeout("tcp", ollamaHost, 2*time.Second); err != nil {
		t.Fatalf("no ollama on %s: %v\nStart it with `brew services start ollama` and "+
			"`ollama pull %s`", ollamaHost, err, localModel)
	} else {
		conn.Close()
	}

	client := New(Config{ProviderID: "ollama", BaseURL: ollamaURL})
	req := ask()
	req.Model = localModel
	req.MaxTokens = 64
	req.System = []llm.SystemBlock{{Text: "Answer in one short sentence."}}
	req.Messages = []llm.Message{{
		Role:    llm.RoleUser,
		Content: []llm.Block{{Type: llm.BlockText, Text: "Why is the sky blue?"}},
	}}

	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()

	events, err := llm.CheckStream(client.Stream(ctx, req))
	transcribe(t, events)
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}

	end := last(t, events)
	if end.Type != llm.EventDone {
		t.Fatalf("terminal event = %s: %v", end.Type, end.Err)
	}
	if len(end.Partial.Content) == 0 || end.Partial.Content[0].Text == "" {
		t.Fatalf("content = %+v, want the reply", end.Partial.Content)
	}
	if usage := end.Partial.Usage; usage.Input <= 0 || usage.Output <= 0 {
		t.Errorf("usage = %+v; this server honours stream_options.include_usage, and a version that "+
			"stops should say so here rather than degrade quietly", usage)
	}
}

func liveOnly(t *testing.T) {
	t.Helper()
	if os.Getenv(liveVar) == "" {
		t.Skipf("live endpoint check; set %s=1 to run it (`just verify-openaicompat`)", liveVar)
	}
}

func liveCredential(t *testing.T, name string) string {
	t.Helper()
	liveOnly(t)
	key := os.Getenv(name)
	if key == "" {
		t.Fatalf("%s is set but %s is not; the live check has nothing to authenticate with. "+
			"Run it through `doppler run -- just verify-openaicompat`", liveVar, name)
	}
	return key
}

// transcribe writes the stream out event by event, so a live run leaves a record
// of what the endpoint actually sent rather than only whether it passed.
func transcribe(t *testing.T, events []llm.Event) {
	t.Helper()
	for i, ev := range events {
		var detail []string
		if ev.Delta != "" {
			detail = append(detail, fmt.Sprintf("delta=%q", ev.Delta))
		}
		if ev.ToolCall != nil {
			detail = append(detail, fmt.Sprintf("call=%s(%s) args=%s",
				ev.ToolCall.Name, ev.ToolCall.ID, ev.ToolCall.Input))
		}
		if ev.StopReason != "" {
			detail = append(detail, "stop="+string(ev.StopReason))
		}
		if ev.Err != nil {
			detail = append(detail, "err="+ev.Err.Error())
		}
		t.Logf("%2d %-16s %s", i, ev.Type, strings.Join(detail, " "))
	}
	if len(events) == 0 {
		return
	}
	end := events[len(events)-1].Partial
	t.Logf("   message: model=%s provider=%s usage=%+v", end.Model, end.Provider, end.Usage)
	for i, block := range end.Content {
		t.Logf("   block %d: %s %s%s %s", i, block.Type, block.Name, block.Text, block.Input)
	}
}
