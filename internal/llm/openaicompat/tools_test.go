package openaicompat

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/llm"
)

// TestStreamToolCall is the whole normalization in one turn. The wire has no
// per-block stop event and no block index: a call is an entry in a `tool_calls`
// array, its function name arrives only on the first fragment, and its arguments
// are a string split at arbitrary byte boundaries. What comes out is the same
// event union Anthropic produces.
func TestStreamToolCall(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "tool_call.sse").Stream(context.Background(), askWithTools()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}

	want := []llm.EventType{
		llm.EventTextDelta,
		llm.EventToolInputStart,
		llm.EventToolInputDelta, llm.EventToolInputDelta, llm.EventToolInputDelta,
		llm.EventToolCall,
		llm.EventDone,
	}
	if got := types(events); !slices.Equal(got, want) {
		t.Errorf("event types = %v, want %v", got, want)
	}

	// The fragments the wire sent, verbatim — and only those. This endpoint opens a
	// call's arguments with two empty strings, which announce nothing and would have
	// a UI drawing an argument stream that never moved.
	var deltas []string
	for _, ev := range events {
		if ev.Type == llm.EventToolInputDelta {
			deltas = append(deltas, ev.Delta)
		}
	}
	if got := strings.Join(deltas, "|"); got != `{"pa|th": "au|th.go"}` {
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
		{Type: llm.BlockToolUse, ID: "call_read_auth", Name: "read", Input: json.RawMessage(`{"path": "auth.go"}`)},
	}
	if got := end.Partial.Content; !reflect.DeepEqual(got, wantContent) {
		t.Fatalf("committed content = %+v, want %+v", got, wantContent)
	}

	call := announcement(t, events, "call_read_auth")
	if call.Name != "read" || string(call.Input) != `{"path": "auth.go"}` {
		t.Errorf("announced call = %+v", call)
	}
}

// TestStreamRoutesFragmentsToTheirOwnCall is the mis-route design §3.1a says the
// contract cannot catch: two calls whose fragments both parse, so an adapter that
// appended call 2's tail onto call 1 passes every rule and runs `read a.gob.go`.
// Closing it needs the wire and the neutral message compared as two independent
// things, which is what this does.
//
// Both fixtures carry the same two calls. The second is the shape design §3.1a
// names outright — one chunk carrying two `tool_calls` entries — and it is also
// where the two announcement paths part: its content field never changes state, so
// the accumulator reports one call finished and the end-of-stream sweep finds the
// other.
func TestStreamRoutesFragmentsToTheirOwnCall(t *testing.T) {
	wantContent := []llm.Block{
		{Type: llm.BlockToolUse, ID: "call_first", Name: "read", Input: json.RawMessage(`{"path": "a.go"}`)},
		{Type: llm.BlockToolUse, ID: "call_second", Name: "read", Input: json.RawMessage(`{"path": "b.go"}`)},
	}

	for name, fixture := range map[string]string{
		"one call at a time":            "parallel_tool_calls.sse",
		"both calls in the same chunks": "interleaved_tool_calls.sse",
	} {
		t.Run(name, func(t *testing.T) {
			events, err := llm.CheckStream(replay(t, fixture).Stream(context.Background(), askWithTools()))
			if err != nil {
				t.Fatalf("CheckStream: %v", err)
			}
			if got := announced(events); !slices.Equal(got, []string{"call_first", "call_second"}) {
				t.Errorf("announced %v, want each call once, in block order", got)
			}
			end := last(t, events)
			if got := end.Partial.Content; !reflect.DeepEqual(got, wantContent) {
				t.Fatalf("committed content = %+v, want %+v", got, wantContent)
			}
			if end.StopReason != llm.StopToolUse {
				t.Errorf("stop reason = %q, want %q", end.StopReason, llm.StopToolUse)
			}
			// One block per delta event. A chunk carrying two entries is faithful and
			// a delta event that grew two blocks is not, so the events are per entry.
			for i, ev := range events {
				if ev.Type != llm.EventToolInputDelta {
					continue
				}
				if ev.Delta == "" {
					t.Errorf("event %d is an argument delta with nothing in it", i)
				}
			}
		})
	}
}

// TestStreamAnnouncesACallOnlyTheSweepCatches: JustFinishedToolCall fires on a
// state transition, so a dialect whose last chunk carries the final fragment AND
// the finish reason never transitions. Without the sweep the model asks for a tool
// and nothing runs it — and the stream is otherwise perfectly well formed, so
// nothing else notices.
func TestStreamAnnouncesACallOnlyTheSweepCatches(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "tool_call_in_final_chunk.sse").Stream(context.Background(), askWithTools()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	if got := announced(events); !slices.Equal(got, []string{"call_last"}) {
		t.Fatalf("announced %v, want the one call the turn asked for", got)
	}
	call := announcement(t, events, "call_last")
	if string(call.Input) != `{"path":"auth.go"}` {
		t.Errorf("announced arguments = %s", call.Input)
	}
}

// TestStreamToolCallNameArrivesInPieces: the SDK's accumulator concatenates a
// call's function name across fragments, which is only worth doing if a name can
// arrive split. Taking the name once, when the block opens, commits its first half
// — and `multi` resolves to nothing in the registry, so the turn fails naming a
// tool the model never asked for.
func TestStreamToolCallNameArrivesInPieces(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "tool_call_split_name.sse").
		Stream(context.Background(), askWithTools()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	call := announcement(t, events, "call_split")
	if call.Name != "multi_edit" {
		t.Errorf("announced call names %q, want the whole name", call.Name)
	}
	if got := last(t, events).Partial.Content[0].Name; got != "multi_edit" {
		t.Errorf("committed block names %q, want the whole name", got)
	}
}

// TestStreamToolCallWithNoArguments: this wire opens a call's arguments with an
// empty string, and a call that takes none never sends another fragment — so the
// finished payload and the not-yet-started one are the same bytes. The call still
// has to be announced, and with `{}` rather than nothing: `null` unmarshals into a
// struct as a silent no-op and would run a tool with every argument zeroed.
func TestStreamToolCallWithNoArguments(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "tool_call_no_arguments.sse").Stream(context.Background(), askWithTools()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	want := []llm.EventType{llm.EventToolInputStart, llm.EventToolCall, llm.EventDone}
	if got := types(events); !slices.Equal(got, want) {
		t.Errorf("event types = %v, want %v", got, want)
	}

	call := announcement(t, events, "call_bare")
	if call.Name != "pwd" || string(call.Input) != "{}" {
		t.Errorf("announced call = %+v, want empty arguments", call)
	}
	wantContent := []llm.Block{
		{Type: llm.BlockToolUse, ID: "call_bare", Name: "pwd", Input: json.RawMessage("{}")},
	}
	if got := last(t, events).Partial.Content; !reflect.DeepEqual(got, wantContent) {
		t.Fatalf("committed content = %+v, want %+v", got, wantContent)
	}
}

// TestStreamTruncatedToolCall walks the whole round trip, because the hazard is
// spread across it. The output limit cuts the arguments mid-object; this wire ends
// the turn with the same shape a finished one gets, so announcing on it would
// dispatch a call whose arguments parse and mean something else. The fragment is
// committed all the same — dropping the block would move an index — and the next
// request is what has to survive holding one.
func TestStreamTruncatedToolCall(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "truncated_tool_call.sse").Stream(context.Background(), askWithTools()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	want := []llm.EventType{
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
		{Type: llm.BlockToolUse, ID: "call_cut", Name: "write", Input: json.RawMessage(fragment)},
	}
	committed := *end.Partial
	if !reflect.DeepEqual(committed.Content, wantContent) {
		t.Fatalf("committed content = %+v, want the fragment as it arrived (%+v)", committed.Content, wantContent)
	}

	// Now the request built from that transcript. The arguments go on this wire as a
	// string, so the fragment would be sent as arguments the model never finished.
	next := askWithTools()
	next.Messages = append(next.Messages, committed, llm.Message{
		Role: llm.RoleUser,
		Content: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "call_cut",
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
	if !strings.Contains(body, `"arguments":"{}"`) {
		t.Errorf("the truncated call went out without the empty arguments standing in for it: %s", body)
	}
}

// TestStreamTruncationSilencesACallThatParses is the same guard where nothing else
// would catch it. The cut lands after a complete second call, so its arguments
// parse and validate — design §4 invariant 2's whole point — and the finish reason
// is the only thing that says the turn stopped rather than ended.
//
// The first call is announced, because it finished before the cut, and the loop
// fails it along with the rest of a truncated turn.
func TestStreamTruncationSilencesACallThatParses(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "truncated_after_a_complete_call.sse").
		Stream(context.Background(), askWithTools()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	end := last(t, events)
	if end.StopReason != llm.StopMaxTokens {
		t.Fatalf("stop reason = %q, want %q", end.StopReason, llm.StopMaxTokens)
	}
	if got := announced(events); !slices.Equal(got, []string{"call_first"}) {
		t.Errorf("announced %v, want only the call that finished before the cut", got)
	}
	// Both blocks are committed either way: the loop writes a failing result for
	// each, and dropping one would move an index a consumer has already drawn.
	if got := len(end.Partial.Content); got != 2 {
		t.Errorf("committed %d blocks, want both calls: %+v", got, end.Partial.Content)
	}
}

// TestStreamRefusesAFinishedCallWithArgumentsThatDoNotParse: the model wrote
// something that is not JSON and the endpoint called the turn finished. Neither
// answer is available — announcing runs a tool with arguments that mean nothing,
// and staying quiet leaves a tool_use block the loop never answers, which fails
// every later request in the session rather than this one.
//
// This is also the case that keeps the parse check in ready() alive: truncation
// is caught a step earlier by the finish reason, so without this fixture the check
// could be deleted with the suite green.
func TestStreamRefusesAFinishedCallWithArgumentsThatDoNotParse(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "tool_call_bad_arguments.sse").
		Stream(context.Background(), askWithTools()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	end := last(t, events)
	if end.Type != llm.EventError || end.StopReason != llm.StopError {
		t.Fatalf("terminal event = %s/%s, want %s/%s", end.Type, end.StopReason, llm.EventError, llm.StopError)
	}
	if end.Err == nil || !strings.Contains(end.Err.Error(), "path=auth.go") {
		t.Errorf("error = %v, want one carrying what the model actually wrote", end.Err)
	}
	if got := announced(events); len(got) != 0 {
		t.Errorf("announced %v; the arguments never became a JSON object", got)
	}
}

// TestStreamRefusesACallWithNoID: a tool_use block with no id leaves the
// tool_result answering it nowhere to point, and design §4 invariant 1 rests on
// that pairing — so a session holding one fails every later request rather than
// this one. Committing it quietly is the version that looks like it worked.
func TestStreamRefusesACallWithNoID(t *testing.T) {
	events, err := llm.CheckStream(replay(t, "tool_call_no_id.sse").Stream(context.Background(), askWithTools()))
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	end := last(t, events)
	if end.Type != llm.EventError || end.StopReason != llm.StopError {
		t.Fatalf("terminal event = %s/%s, want %s/%s", end.Type, end.StopReason, llm.EventError, llm.StopError)
	}
	if end.Err == nil || !strings.Contains(end.Err.Error(), "no id") {
		t.Errorf("error = %v, want one saying the call could not be answered", end.Err)
	}
	if got := announced(events); len(got) != 0 {
		t.Errorf("announced %v; a call nothing can answer is not one to run", got)
	}
	// And it is not in the message either. Committed, it would fail every later
	// request built from that transcript rather than only this turn.
	if got := len(end.Partial.Content); got != 0 {
		t.Errorf("committed %d blocks, want none: %+v", got, end.Partial.Content)
	}
}

// TestRequestSendsToolDefinitions reads the bytes rather than the params struct.
// Every keyword goes out, not only the ones the SDK has fields for: a schema that
// lost additionalProperties still describes the same arguments loosely enough to
// look right, and the model would be told the wrong thing about the ones it may
// invent.
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
		Type     string `json:"type"`
		Function struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatalf("decoding the tools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("sent %d tool definitions, want 1: %s", len(tools), raw)
	}
	if tools[0].Type != "function" || tools[0].Function.Name != "read" ||
		tools[0].Function.Description != "Read a span of a file" {
		t.Errorf("tool = %+v", tools[0])
	}
	want := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"path": map[string]any{"type": "string", "description": "Which file"}},
		"required":             []any{"path"},
		"additionalProperties": false,
	}
	if !reflect.DeepEqual(tools[0].Function.Parameters, want) {
		t.Errorf("parameters = %+v, want %+v", tools[0].Function.Parameters, want)
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
	req := ask()
	req.Tools = []llm.ToolSpec{{Description: "read a file"}}

	_, err := buildParams(req)
	if err == nil {
		t.Fatal("no error; the tool would go out with no name, and nothing could be resolved from an answer naming none")
	}
	if !strings.Contains(err.Error(), "no name") {
		t.Errorf("error = %v", err)
	}
}

// TestReplayingAToolTurn is the pairing on the wire, and the shape change this
// adapter has to make: the block model carries a turn's tool results in one user
// message, where this API wants one `tool` message each, directly after the
// assistant message that made the calls.
func TestReplayingAToolTurn(t *testing.T) {
	req := askWithTools()
	req.Messages = append(req.Messages,
		llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopToolUse, Content: []llm.Block{
			{Type: llm.BlockText, Text: "I'll read them."},
			{Type: llm.BlockToolUse, ID: "call_a", Name: "read", Input: json.RawMessage(`{"path":"a.go"}`)},
			{Type: llm.BlockToolUse, ID: "call_b", Name: "read", Input: json.RawMessage(`{"path":"b.go"}`)},
		}},
		llm.Message{Role: llm.RoleUser, Content: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: "call_a", Content: "package a"},
			{Type: llm.BlockToolResult, ToolUseID: "call_b", Content: "package b"},
		}},
	)

	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	wire := decodeMessages(t, params)
	if got := roles(wire); !slices.Equal(got, []string{"user", "assistant", "tool", "tool"}) {
		t.Fatalf("roles on the wire = %v", got)
	}
	if got := wire[1].Content; got != "I'll read them." {
		t.Errorf("assistant content = %q", got)
	}
	if len(wire[1].ToolCalls) != 2 {
		t.Fatalf("the assistant message carried %d tool calls, want 2", len(wire[1].ToolCalls))
	}
	for i, want := range []struct{ id, name, args string }{
		{"call_a", "read", `{"path":"a.go"}`},
		{"call_b", "read", `{"path":"b.go"}`},
	} {
		call := wire[1].ToolCalls[i]
		if call.ID != want.id || call.Function.Name != want.name || call.Function.Arguments != want.args {
			t.Errorf("tool call %d = %+v, want %+v", i, call, want)
		}
	}
	// The pairing: each result names the call it answers, and the API rejects a
	// `tool` message that names none (design §4 invariant 1).
	for i, want := range []struct{ id, content string }{
		{"call_a", "package a"},
		{"call_b", "package b"},
	} {
		msg := wire[2+i]
		if msg.ToolCallID != want.id || msg.Content != want.content {
			t.Errorf("tool message %d = %+v, want %+v", i, msg, want)
		}
	}
}

func TestBuildParamsRejectsAnUnpairableToolBlock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		msg   llm.Message
		block llm.Block
		want  string
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
			block: llm.Block{Type: llm.BlockToolUse, ID: "call_a"},
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
