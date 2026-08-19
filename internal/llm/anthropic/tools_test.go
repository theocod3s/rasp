package anthropic

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/theocod3s/rasp/internal/llm"
)

func TestStreamToolCall(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "tool_call.sse").Stream(context.Background(), askWithTools()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}

	want := []llm.EventType{
		llm.EventMessageStart,
		llm.EventTextDelta,
		llm.EventToolInputStart,
		llm.EventToolInputDelta, llm.EventToolInputDelta, llm.EventToolInputDelta, llm.EventToolInputDelta,
		llm.EventToolCall,
		llm.EventDone,
	}
	if got := types(events); !slices.Equal(got, want) {
		t.Errorf("event types = %v, want %v", got, want)
	}

	// The fragments the wire sent, verbatim. The first is empty on purpose: the
	// API opens a tool block's fragment stream with one, and an adapter that
	// swallowed it would be hiding a delta the contract allows.
	var deltas []string
	for _, ev := range events {
		if ev.Type == llm.EventToolInputDelta {
			deltas = append(deltas, ev.Delta)
		}
	}
	if got := strings.Join(deltas, "|"); got != `|{"pa|th": "au|th.go"}` {
		t.Errorf("argument fragments = %q", got)
	}

	end := last(t, events)
	if end.StopReason != llm.StopToolUse {
		t.Errorf("stop reason = %q, want %q", end.StopReason, llm.StopToolUse)
	}
	// Asserted against the fixture rather than against the events: this is the one
	// place the wire and the neutral message are two independent things to compare,
	// which is what the stream contract deliberately cannot check (design §3.1a).
	wantContent := []llm.Block{
		{Type: llm.BlockText, Text: "I'll read it."},
		{Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "read", Input: json.RawMessage(`{"path": "auth.go"}`)},
	}
	if got := end.Partial.Content; !reflect.DeepEqual(got, wantContent) {
		t.Fatalf("committed content = %+v, want %+v", got, wantContent)
	}

	call := announcement(t, events, "toolu_01A9")
	if call.Name != "read" || string(call.Input) != `{"path": "auth.go"}` {
		t.Errorf("announced call = %+v", call)
	}
}

// TestStreamAnnouncesToolCallsInBlockOrder runs a two-call turn twice, closing
// its blocks in each order. Both have to come out the same: results are answered
// in announcement order and persisted in block order, and the two have to agree
// or the provider rejects the next request.
//
// The out-of-order fixture is the one the SDK forces — it documents that stop
// events interleave across open blocks and address them by index, so block 1 can
// close first. The in-order fixture is the ordinary shape, and it is what catches
// a call announced a second time when a later block closes.
func TestStreamAnnouncesToolCallsInBlockOrder(t *testing.T) {
	wantEvents := []llm.EventType{
		llm.EventMessageStart,
		llm.EventToolInputStart, llm.EventToolInputStart,
		llm.EventToolInputDelta, llm.EventToolInputDelta, llm.EventToolInputDelta,
		llm.EventToolCall, llm.EventToolCall,
		llm.EventDone,
	}
	wantContent := []llm.Block{
		{Type: llm.BlockToolUse, ID: "toolu_first", Name: "read", Input: json.RawMessage(`{"path": "a.go"}`)},
		{Type: llm.BlockToolUse, ID: "toolu_second", Name: "read", Input: json.RawMessage(`{"path": "b.go"}`)},
	}

	for name, fixture := range map[string]string{
		"the second block closes first": "parallel_tool_calls.sse",
		"the blocks close in order":     "parallel_tool_calls_in_order.sse",
	} {
		t.Run(name, func(t *testing.T) {
			events, err := llm.CheckStream(replay(t, fixture).Stream(context.Background(), askWithTools()))
			if err != nil {
				t.Fatalf("CheckStream: %v", err)
			}
			// Sorted by type rather than compared in place: the in-order fixture
			// opens its second block after the first has closed, so only the counts
			// are shared between the two.
			got, want := slices.Clone(types(events)), slices.Clone(wantEvents)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("event types = %v, want %v", types(events), wantEvents)
			}

			var announced []string
			for _, ev := range events {
				if ev.Type == llm.EventToolCall {
					announced = append(announced, ev.ToolCall.ID)
				}
			}
			if !slices.Equal(announced, []string{"toolu_first", "toolu_second"}) {
				t.Errorf("announced %v, want each call once, in the wire's block order", announced)
			}
			if got := last(t, events).Partial.Content; !reflect.DeepEqual(got, wantContent) {
				t.Fatalf("committed content = %+v, want %+v", got, wantContent)
			}
		})
	}
}

// TestStreamToolCallWithNoArguments covers the shape where the empty object a
// block opens with and the arguments themselves are the same string. The block
// never observably changes, so nothing here can be inferred from the accumulation
// — the call still has to be announced, and with `{}` rather than nothing.
func TestStreamToolCallWithNoArguments(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "tool_call_no_arguments.sse").Stream(context.Background(), askWithTools()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}

	want := []llm.EventType{
		llm.EventMessageStart,
		llm.EventToolInputStart, llm.EventToolInputDelta,
		llm.EventToolCall,
		llm.EventDone,
	}
	if got := types(events); !slices.Equal(got, want) {
		t.Errorf("event types = %v, want %v", got, want)
	}

	call := announcement(t, events, "toolu_bare")
	if call.Name != "pwd" || string(call.Input) != "{}" {
		t.Errorf("announced call = %+v, want empty arguments", call)
	}
	wantContent := []llm.Block{
		{Type: llm.BlockToolUse, ID: "toolu_bare", Name: "pwd", Input: json.RawMessage("{}")},
	}
	if got := last(t, events).Partial.Content; !reflect.DeepEqual(got, wantContent) {
		t.Fatalf("committed content = %+v, want %+v", got, wantContent)
	}
}

// TestStreamTruncatedToolCall walks the whole round trip, because the hazard is
// spread across it. The output limit cuts the arguments mid-object; the API ends
// that block with the same stop event a finished one gets, so announcing on the
// stop would dispatch a call whose arguments parse and mean something else. The
// fragment is committed all the same — dropping the block would move an index —
// and the next request is what has to survive holding one.
func TestStreamTruncatedToolCall(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "truncated_tool_call.sse").Stream(context.Background(), askWithTools()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}

	want := []llm.EventType{
		llm.EventMessageStart,
		llm.EventTextDelta,
		llm.EventToolInputStart, llm.EventToolInputDelta,
		llm.EventDone,
	}
	if got := types(events); !slices.Equal(got, want) {
		t.Errorf("event types = %v, want %v; a truncated call is one nothing may run", got, want)
	}
	end := last(t, events)
	if end.StopReason != llm.StopMaxTokens {
		t.Fatalf("stop reason = %q, want %q", end.StopReason, llm.StopMaxTokens)
	}

	fragment := `{"path": "main.go", "content": "package m`
	wantContent := []llm.Block{
		{Type: llm.BlockText, Text: "Writing it now."},
		{Type: llm.BlockToolUse, ID: "toolu_cut", Name: "write", Input: json.RawMessage(fragment)},
	}
	committed := *end.Partial
	if !reflect.DeepEqual(committed.Content, wantContent) {
		t.Fatalf("committed content = %+v, want the fragment as it arrived (%+v)", committed.Content, wantContent)
	}

	// Now the request built from that transcript. json.Marshal refuses a fragment,
	// and it refuses the whole message with it, so a session holding one would
	// send nothing at all from here on.
	next := askWithTools()
	next.Messages = append(next.Messages, committed, llm.Message{
		Role: llm.RoleUser,
		Content: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "toolu_cut",
			Content:   "the reply was cut off before the arguments finished arriving",
			IsError:   true,
		}},
	})
	params, err := buildParams(next)
	if err != nil {
		t.Fatalf("buildParams: %v; every later request in this session would fail the same way", err)
	}
	body := requestBody(t, params)
	if strings.Contains(body, "package m") {
		t.Errorf("the fragment went out as arguments: %s", body)
	}
	if !strings.Contains(body, `"input":{}`) {
		t.Errorf("the truncated call went out without the empty arguments standing in for it: %s", body)
	}
}

// TestStreamBlockStopOutOfRange: a stop event addressing a block that never
// opened is the shape the adapter indexes the accumulator with, so getting this
// wrong is a panic inside a provider goroutine rather than a failed turn. The
// SDK rejects it first, which is why nothing here re-checks the index — and this
// is what says so out loud if that ever stops being true.
func TestStreamBlockStopOutOfRange(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "bad_block_index.sse").Stream(context.Background(), askWithTools()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	end := last(t, events)
	if end.Type != llm.EventError || end.StopReason != llm.StopError {
		t.Fatalf("terminal event = %s/%s, want %s/%s", end.Type, end.StopReason, llm.EventError, llm.StopError)
	}
	if end.Err == nil || !strings.Contains(end.Err.Error(), "content block") {
		t.Errorf("error = %v, want one naming the block the stop event pointed at", end.Err)
	}
}

// TestRequestSendsToolDefinitions reads the bytes rather than the params struct:
// the schema travels in an extras map, which is exactly the shape whose absence
// is invisible from the Go side.
func TestRequestSendsToolDefinitions(t *testing.T) {
	client := replay(t, "tool_call.sse")
	for range client.Stream(context.Background(), askWithTools()) {
	}

	sent := client.sent(t)
	raw, err := json.Marshal(sent["tools"])
	if err != nil {
		t.Fatalf("re-encoding the tools: %v", err)
	}
	var tools []struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"input_schema"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatalf("decoding the tools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("sent %d tool definitions, want 1: %s", len(tools), raw)
	}
	if tools[0].Name != "read" || tools[0].Description != "Read a span of a file" {
		t.Errorf("tool = %+v", tools[0])
	}

	// Every keyword, not only the two the SDK has fields for. A schema that lost
	// additionalProperties still describes the same arguments loosely enough to
	// look right, and the model would be told the wrong thing about the ones it
	// may invent.
	want := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"path": map[string]any{"type": "string", "description": "Which file"}},
		"required":             []any{"path"},
		"additionalProperties": false,
	}
	if !reflect.DeepEqual(tools[0].InputSchema, want) {
		t.Errorf("input_schema = %+v, want %+v", tools[0].InputSchema, want)
	}
}

// TestBuildParamsLeavesTheToolSchemaAlone: Request.Tools comes from one registry
// snapshot per turn and the caller keeps holding it, so a schema edited in place
// here would follow the tool for the rest of the session.
func TestBuildParamsLeavesTheToolSchemaAlone(t *testing.T) {
	req := askWithTools()
	before := marshalled(t, req.Tools[0].Schema)

	if _, err := buildParams(req); err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	if got := marshalled(t, req.Tools[0].Schema); got != before {
		t.Errorf("building the request rewrote the tool's schema:\nbefore %s\n after %s", before, got)
	}
}

func TestBuildParamsRejectsAToolItCannotDescribe(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec llm.ToolSpec
		want string
	}{
		{"no name", llm.ToolSpec{Description: "read a file"}, "no name"},
		{
			// The SDK writes `"type":"object"` itself, so a schema saying otherwise
			// would go out relabelled and the tool would be called with arguments
			// its own unmarshal target cannot read.
			"a schema that is not an object",
			llm.ToolSpec{Name: "read", Schema: map[string]any{"type": "array"}},
			"object schema",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := ask()
			req.Tools = []llm.ToolSpec{tc.spec}

			_, err := buildParams(req)
			if err == nil {
				t.Fatal("no error; the tool would go out described as something it is not")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// TestReplayingAToolTurn is the pairing on the wire: the assistant turn that
// asked, and the user turn that answered, in one request. Sending either without
// the other is a 400 on this request and on every one built from the transcript
// afterwards (design §4 invariant 1).
func TestReplayingAToolTurn(t *testing.T) {
	req := askWithTools()
	req.Messages = append(req.Messages,
		llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopToolUse, Content: []llm.Block{
			{Type: llm.BlockText, Text: "I'll read it."},
			{Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "read", Input: json.RawMessage(`{"path":"auth.go"}`)},
		}},
		llm.Message{Role: llm.RoleUser, Content: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: "toolu_01A9", Content: "package auth"},
		}},
	)

	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	if got := len(params.Messages); got != 3 {
		t.Fatalf("sent %d messages, want the ask, the tool turn and its results", got)
	}
	assertRolesAlternate(t, params)

	uses, results := toolBlocks(t, params)
	if !reflect.DeepEqual(uses, map[string]string{"toolu_01A9": `{"path":"auth.go"}`}) {
		t.Errorf("tool_use blocks on the wire = %v", uses)
	}
	if !reflect.DeepEqual(results, map[string]string{"toolu_01A9": "package auth"}) {
		t.Errorf("tool_result blocks on the wire = %v", results)
	}
}

// TestSendingAToolResultWithNoOutput: a tool that succeeds and prints nothing is
// ordinary, and the two ways of sending it are not equivalent — the content list
// is optional, an empty text block inside it is rejected like any other.
func TestSendingAToolResultWithNoOutput(t *testing.T) {
	req := ask()
	req.Messages = append(req.Messages,
		llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockToolUse, ID: "toolu_quiet", Name: "bash", Input: json.RawMessage(`{"command":"true"}`)},
		}},
		llm.Message{Role: llm.RoleUser, Content: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: "toolu_quiet"},
		}},
	)

	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	var decoded struct {
		Messages []struct {
			Content []struct {
				Type    string `json:"type"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(requestBody(t, params)), &decoded); err != nil {
		t.Fatalf("decoding the request body: %v", err)
	}

	seen := 0
	for _, msg := range decoded.Messages {
		for _, block := range msg.Content {
			if block.Type != "tool_result" {
				continue
			}
			seen++
			if len(block.Content) != 0 {
				t.Errorf("the empty result went out carrying %+v, which the API rejects", block.Content)
			}
		}
	}
	if seen != 1 {
		t.Fatalf("examined %d tool_result blocks, want 1; the shape decoded here no longer matches "+
			"what the SDK emits: %s", seen, requestBody(t, params))
	}
}

func TestBuildParamsRejectsAnUnpairableToolBlock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		msg   llm.Message
		want  string
		block llm.Block
	}{
		{
			name: "a tool_use with no id",
			msg:  llm.Message{Role: llm.RoleAssistant},
			block: llm.Block{Type: llm.BlockToolUse, Name: "read",
				Input: json.RawMessage(`{"path":"auth.go"}`)},
			want: "tool_use block",
		},
		{
			name:  "a tool_use with no name",
			msg:   llm.Message{Role: llm.RoleAssistant},
			block: llm.Block{Type: llm.BlockToolUse, ID: "toolu_01A9"},
			want:  "tool_use block",
		},
		{
			name:  "a tool_result naming no call",
			msg:   llm.Message{Role: llm.RoleUser},
			block: llm.Block{Type: llm.BlockToolResult, Content: "package auth"},
			want:  "tool_result block names no tool_use",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := ask()
			tc.msg.Content = []llm.Block{tc.block}
			req.Messages = append(req.Messages, tc.msg)

			_, err := buildParams(req)
			if err == nil {
				t.Fatal("no error; the pairing design §4 invariant 1 rests on broke at the last layer that could still see it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// askWithTools is ask() with the one tool the fixtures were recorded against.
func askWithTools() llm.Request {
	req := ask()
	req.Tools = []llm.ToolSpec{{
		Name:        "read",
		Description: "Read a span of a file",
		Schema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"path": map[string]any{"type": "string", "description": "Which file"}},
			"required":             []any{"path"},
			"additionalProperties": false,
		},
	}}
	return req
}

// announcement is the EventToolCall for id, and a failure if the stream never
// announced one — a missing announcement is silence, and silence reads as a pass.
func announcement(t *testing.T, events []llm.Event, id string) *llm.ToolCall {
	t.Helper()
	for _, ev := range events {
		if ev.Type == llm.EventToolCall && ev.ToolCall.ID == id {
			return ev.ToolCall
		}
	}
	t.Fatalf("no EventToolCall announced %q; the loop dispatches from the event, so a call only in "+
		"the message is one nothing runs", id)
	return nil
}

// toolBlocks is what the request carries, keyed by call id: the tool_use blocks
// with their arguments, and the tool_result blocks with their text.
func toolBlocks(t *testing.T, params sdk.MessageNewParams) (uses, results map[string]string) {
	t.Helper()
	var decoded struct {
		Messages []struct {
			Content []struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				Input     json.RawMessage `json:"input"`
				ToolUseID string          `json:"tool_use_id"`
				Content   []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	body := requestBody(t, params)
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decoding the request body: %v", err)
	}

	uses, results = map[string]string{}, map[string]string{}
	for _, msg := range decoded.Messages {
		for _, block := range msg.Content {
			switch block.Type {
			case "tool_use":
				uses[block.ID] = string(block.Input)
			case "tool_result":
				text := ""
				for _, part := range block.Content {
					text += part.Text
				}
				results[block.ToolUseID] = text
			}
		}
	}
	if len(uses) == 0 && len(results) == 0 {
		t.Fatalf("the request carried no tool blocks, so nothing was examined: %s", body)
	}
	return uses, results
}

func requestBody(t *testing.T, params sdk.MessageNewParams) string {
	t.Helper()
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshalling the params: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("the params marshalled to nothing, so every check against them would pass")
	}
	return string(body)
}

func marshalled(t *testing.T, v any) string {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling %v: %v", v, err)
	}
	return string(out)
}
