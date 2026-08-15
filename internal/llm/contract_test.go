package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"slices"
	"testing"

	"github.com/theocod3s/rasp/internal/llm"
)

var _ llm.Provider = scripted{}

// TestStreamHasNoErrorResult checks the "never returns a Go error" half of the
// contract over the interface. Reflection rather than a compile-time assertion,
// because adding an error result breaks the build at every implementation, which
// reads as "update the adapters" rather than as "every call site now has a second
// error path to forget about".
func TestStreamHasNoErrorResult(t *testing.T) {
	method, ok := reflect.TypeFor[llm.Provider]().MethodByName("Stream")
	if !ok {
		t.Fatal("Provider has no Stream method")
	}

	if got := method.Type.NumOut(); got != 1 {
		t.Fatalf("Provider.Stream returns %d values, want exactly 1: model, request and "+
			"runtime failures arrive as a terminal EventError, not as a Go error", got)
	}
	if got, want := method.Type.Out(0), reflect.TypeFor[llm.StreamResponse](); got != want {
		t.Fatalf("Provider.Stream returns %s, want %s", got, want)
	}
	if got, want := reflect.TypeFor[llm.StreamResponse](), reflect.TypeFor[iter.Seq[llm.Event]](); got != want {
		t.Fatalf("StreamResponse is %s, want it to stay the same type as %s", got, want)
	}

}

// TestEveryEventCarriesTheAccumulatedMessage drives a stream that thinks, speaks
// and calls a tool.
func TestEveryEventCarriesTheAccumulatedMessage(t *testing.T) {
	provider := scripted{id: "test", actions: []action{
		thinking("The test ", "is in auth."),
		text("I'll", " read", " it."),
		toolCall("toolu_01A9", "read", `{"pa`, `th": "au`, `th.go"}`),
		done(llm.StopToolUse),
	}}

	events := check(t, provider.Stream(context.Background(), llm.Request{}))

	want := []llm.EventType{
		llm.EventMessageStart,
		llm.EventThinkingDelta, llm.EventThinkingDelta,
		llm.EventTextDelta, llm.EventTextDelta, llm.EventTextDelta,
		llm.EventToolInputStart,
		llm.EventToolInputDelta, llm.EventToolInputDelta, llm.EventToolInputDelta,
		llm.EventToolCall,
		llm.EventDone,
	}
	if got := types(events); !slices.Equal(got, want) {
		t.Fatalf("event sequence:\n got %v\nwant %v", got, want)
	}

	// One message, one address, which is what makes "render Partial" the whole of
	// the UI's logic.
	for i, ev := range events {
		if ev.Partial != events[0].Partial {
			t.Fatalf("event %d (%s) carries a different *Message than event 0; the message is "+
				"allocated once, outside the stream loop", i, ev.Type)
		}
	}

	final := events[0].Partial
	if got, want := final.Role, llm.RoleAssistant; got != want {
		t.Errorf("Role = %q, want %q", got, want)
	}
	if got, want := final.StopReason, llm.StopToolUse; got != want {
		t.Errorf("StopReason = %q, want %q", got, want)
	}
	wantBlocks := []llm.Block{
		{Type: llm.BlockThinking, Text: "The test is in auth."},
		{Type: llm.BlockText, Text: "I'll read it."},
		{Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "read", Input: json.RawMessage(`{"path": "auth.go"}`)},
	}
	if got := final.Content; !reflect.DeepEqual(got, wantBlocks) {
		t.Errorf("content:\n got %+v\nwant %+v", got, wantBlocks)
	}

	// The fragments do not individually parse; the completed call does.
	call := toolCallIn(t, events)
	var args struct{ Path string }
	if err := json.Unmarshal(call.Input, &args); err != nil {
		t.Fatalf("unmarshalling the completed tool call: %v", err)
	}
	if args.Path != "auth.go" {
		t.Errorf("tool call path = %q, want %q", args.Path, "auth.go")
	}
}

// TestTwoToolCallsInOneTurn is the shape parallel tool execution depends on, and
// what makes accumulation harder than it looks: a checker comparing whole
// channels reads the second call's first fragment as a rewrite of the first.
func TestTwoToolCallsInOneTurn(t *testing.T) {
	provider := scripted{id: "test", actions: []action{
		toolCall("toolu_01", "read", `{"path":`, `"auth.go"}`),
		toolCall("toolu_02", "read", `{"path":`, `"auth_test.go"}`),
		done(llm.StopToolUse),
	}}

	events := check(t, provider.Stream(context.Background(), llm.Request{}))

	var calls []*llm.ToolCall
	for _, ev := range events {
		if ev.Type == llm.EventToolCall {
			calls = append(calls, ev.ToolCall)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("stream reported %d completed calls, want 2", len(calls))
	}
	for i, want := range []string{`{"path":"auth.go"}`, `{"path":"auth_test.go"}`} {
		if got := string(calls[i].Input); got != want {
			t.Errorf("call %d input = %s, want %s", i, got, want)
		}
	}

	// Both calls are in the message too, in the order their results have to come
	// back in.
	final := events[0].Partial
	if got, want := len(final.Content), 2; got != want {
		t.Fatalf("message holds %d blocks, want %d", got, want)
	}
	for i, want := range []string{"toolu_01", "toolu_02"} {
		if got := final.Content[i].ID; got != want {
			t.Errorf("block %d id = %q, want %q", i, got, want)
		}
	}
}

// TestScriptedFailureArrivesAsATerminalEventError asserts the shape of the
// failure, including that text streamed before it survives: a UI that has drawn
// half a reply must not erase it.
func TestScriptedFailureArrivesAsATerminalEventError(t *testing.T) {
	overloaded := errors.New("overloaded_error: server is overloaded")
	provider := scripted{id: "test", actions: []action{
		text("Reading the "),
		fail(overloaded, llm.StopError),
		text("rest of it."), // never streamed: a failure ends the stream
	}}

	events := check(t, provider.Stream(context.Background(), llm.Request{}))

	failure := last(t, events)
	if got, want := failure.Type, llm.EventError; got != want {
		t.Fatalf("terminal event is %q, want %q", got, want)
	}
	if got, want := failure.StopReason, llm.StopError; got != want {
		t.Errorf("StopReason = %q, want %q", got, want)
	}
	if !errors.Is(failure.Err, overloaded) {
		t.Errorf("Err = %v, want it to wrap %v", failure.Err, overloaded)
	}
	// The retry classifier is a pure function over the Message.
	if got, want := failure.Partial.StopReason, llm.StopError; got != want {
		t.Errorf("Partial.StopReason = %q, want %q", got, want)
	}
	if got, want := failure.Partial.Content[0].Text, "Reading the "; got != want {
		t.Errorf("text streamed before the failure = %q, want %q", got, want)
	}
	if got := len(failure.Partial.Content); got != 1 {
		t.Errorf("message holds %d blocks, want 1: the stream ended at the failure", got)
	}
}

// TestCancelledTurnArrivesAsATerminalEventError: not a model error, and still
// only one way out of a stream.
func TestCancelledTurnArrivesAsATerminalEventError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	provider := scripted{id: "test", actions: []action{text("never streamed")}}
	events := check(t, provider.Stream(ctx, llm.Request{}))

	failure := last(t, events)
	if got, want := failure.Type, llm.EventError; got != want {
		t.Fatalf("terminal event is %q, want %q", got, want)
	}
	if got, want := failure.StopReason, llm.StopAborted; got != want {
		t.Errorf("StopReason = %q, want %q", got, want)
	}
	if !errors.Is(failure.Err, context.Canceled) {
		t.Errorf("Err = %v, want context.Canceled", failure.Err)
	}
}

// TestConsumerCanStopEarly covers the pull half of "pull-based iterator"; goleak
// in TestMain is the other half. Two cases, because a provider that checks only
// the first yield, or only the ones inside the script, ignores half the ways a
// turn gets abandoned.
func TestConsumerCanStopEarly(t *testing.T) {
	played := 0
	count := func(*llm.Message, func(llm.Event) bool) bool { played++; return true }

	cases := map[string]int{
		"at the first event": 1, // the stream has barely started
		"part way through":   2, // mid-script, after one text delta
	}

	for name, stopAfter := range cases {
		t.Run(name, func(t *testing.T) {
			played = 0
			provider := scripted{id: "test", actions: []action{text("one"), count, count}}

			seen := 0
			for range provider.Stream(context.Background(), llm.Request{}) {
				seen++
				if seen == stopAfter {
					break
				}
			}

			if seen != stopAfter {
				t.Errorf("consumed %d events, want %d", seen, stopAfter)
			}
			if played != 0 {
				t.Errorf("provider played %d more actions after the consumer stopped, want 0", played)
			}
		})
	}
}

// TestCheckStreamRejects is the contract's own test suite: one malformed stream
// per rule, each asserting the failure names the rule it broke.
func TestCheckStreamRejects(t *testing.T) {
	streamed := func() *llm.Message {
		return &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "I'll"}}}
	}

	called := func(input string) *llm.Message {
		return &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
			Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "read", Input: json.RawMessage(input),
		}}}
	}
	call := func(id, name, input string) *llm.ToolCall {
		return &llm.ToolCall{ID: id, Name: name, Input: json.RawMessage(input)}
	}

	cases := map[string]struct {
		seq  llm.StreamResponse
		want string
	}{
		"no stream at all": {
			seq:  nil,
			want: "nil StreamResponse",
		},
		"an event with no Partial": {
			seq:  stream(llm.Event{Type: llm.EventMessageStart}),
			want: "nil Partial",
		},
		"a message allocated inside the stream loop": {
			seq: func(yield func(llm.Event) bool) {
				// Correct accumulated text, so the only rule broken is the address.
				for _, step := range []struct{ delta, accumulated string }{
					{"I'll", "I'll"},
					{" read", "I'll read"},
				} {
					msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: step.accumulated}}}
					if !yield(llm.Event{Type: llm.EventTextDelta, Delta: step.delta, Partial: msg}) {
						return
					}
				}
			},
			want: "different *Message",
		},
		"a Partial carrying only the delta": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText}}}
				for _, chunk := range []string{"I'll", " read"} {
					msg.Content[0].Text = chunk // = , not +=
					if !yield(llm.Event{Type: llm.EventTextDelta, Delta: chunk, Partial: msg}) {
						return
					}
				}
			},
			want: "never rewritten or dropped",
		},
		"a delta added twice": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText}}}
				msg.Content[0].Text = "I'llI'll"
				yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll", Partial: msg})
			},
			want: "not the delta",
		},
		"two blocks growing on one delta": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
					{Type: llm.BlockText, Text: "one"},
					{Type: llm.BlockText, Text: "two"},
				}}
				yield(llm.Event{Type: llm.EventTextDelta, Delta: "one", Partial: msg})
			},
			want: "grew in 2 blocks at once",
		},
		"thinking text poured into the text block": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "hmm"}}}
				yield(llm.Event{Type: llm.EventThinkingDelta, Delta: "hmm", Partial: msg})
			},
			want: "only [text_delta] adds to it",
		},
		"a thinking delta the message never recorded": {
			seq: func(yield func(llm.Event) bool) {
				yield(llm.Event{
					Type:    llm.EventThinkingDelta,
					Delta:   "hmm",
					Partial: &llm.Message{Role: llm.RoleAssistant},
				})
			},
			want: "Partial thinking",
		},
		"accumulated text emptied by a later event": {
			seq: func(yield func(llm.Event) bool) {
				msg := streamed()
				if !yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll", Partial: msg}) {
					return
				}
				msg.Content[0].Text = ""
				msg.StopReason = llm.StopEndTurn
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: msg})
			},
			want: "never rewritten or dropped",
		},
		"a block removed by a later event": {
			seq: func(yield func(llm.Event) bool) {
				msg := streamed()
				if !yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll", Partial: msg}) {
					return
				}
				msg.Content = nil
				msg.StopReason = llm.StopEndTurn
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: msg})
			},
			want: "blocks are only ever added to",
		},
		// Anthropic's message_delta carries output_tokens and nothing else, so an
		// adapter that assigns where it should merge loses the input count.
		"a usage count revised downward": {
			seq: func(yield func(llm.Event) bool) {
				msg := streamed()
				msg.Usage = llm.Usage{Input: 25, Output: 1}
				if !yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll", Partial: msg}) {
					return
				}
				msg.Usage = llm.Usage{Output: 15}
				msg.StopReason = llm.StopEndTurn
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: msg})
			},
			want: "Usage.Input fell",
		},
		"an event after the terminal one": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopEndTurn}
				if !yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: msg}) {
					return
				}
				yield(llm.Event{Type: llm.EventTextDelta, Delta: "more", Partial: msg})
			},
			want: "after the terminal",
		},
		"a stream that just stops": {
			seq:  stream(llm.Event{Type: llm.EventTextDelta, Delta: "I'll", Partial: streamed()}),
			want: "without a terminal",
		},
		"a stream that yields nothing at all": {
			seq:  stream(),
			want: "no events",
		},
		"a terminal event with no stop reason": {
			seq:  stream(llm.Event{Type: llm.EventDone, Partial: &llm.Message{Role: llm.RoleAssistant}}),
			want: "no StopReason",
		},
		"a stop reason the message does not carry": {
			seq:  stream(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: &llm.Message{Role: llm.RoleAssistant}}),
			want: "what the retry classifier reads",
		},
		"a tool call event with no tool call": {
			seq:  stream(llm.Event{Type: llm.EventToolCall, Partial: &llm.Message{Role: llm.RoleAssistant}}),
			want: "ToolCall is set on EventToolCall",
		},
		"arguments carrying only the last fragment": {
			seq: func(yield func(llm.Event) bool) {
				msg := called("")
				for _, frag := range []string{`{"pa`, `th": "auth.go"}`} {
					msg.Content[0].Input = json.RawMessage(frag) // = , not +=
					if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: frag, Partial: msg}) {
						return
					}
				}
			},
			want: "Partial tool arguments",
		},
		"a completed call whose arguments do not parse": {
			seq: stream(llm.Event{
				Type:     llm.EventToolCall,
				Partial:  called(`{"pa`),
				ToolCall: call("toolu_01A9", "read", `{"pa`),
			}),
			want: "ToolCall arguments do not parse",
		},
		"a completed call the message never mentions": {
			seq: stream(llm.Event{
				Type:     llm.EventToolCall,
				Partial:  &llm.Message{Role: llm.RoleAssistant},
				ToolCall: call("toolu_01A9", "read", `{"path":"auth.go"}`),
			}),
			want: "no tool_use block",
		},
		"a completed call the message names differently": {
			seq: stream(llm.Event{
				Type:     llm.EventToolCall,
				Partial:  called(`{"path":"auth.go"}`),
				ToolCall: call("toolu_01A9", "write", `{"path":"auth.go"}`),
			}),
			want: "the tool_use block with that id names",
		},
		"a completed call with no id": {
			seq: stream(llm.Event{
				Type:     llm.EventToolCall,
				Partial:  called(`{"path":"auth.go"}`),
				ToolCall: call("", "read", `{"path":"auth.go"}`),
			}),
			want: "no ID",
		},
		"a completed call with no name": {
			seq: stream(llm.Event{
				Type:     llm.EventToolCall,
				Partial:  called(`{"path":"auth.go"}`),
				ToolCall: call("toolu_01A9", "", `{"path":"auth.go"}`),
			}),
			want: "no Name",
		},
		"a message left holding the fragments": {
			seq: stream(llm.Event{
				Type:     llm.EventToolCall,
				Partial:  called(`{"pa`),
				ToolCall: call("toolu_01A9", "read", `{"path":"auth.go"}`),
			}),
			want: "holds arguments that do not parse",
		},
		"arguments the event and the message disagree about": {
			seq: stream(llm.Event{
				Type:     llm.EventToolCall,
				Partial:  called(`{"path":"auth.go"}`),
				ToolCall: call("toolu_01A9", "read", `{"path":"/etc/passwd"}`),
			}),
			want: "never ran",
		},
		"arguments that are not an object": {
			seq: stream(llm.Event{
				Type:     llm.EventToolCall,
				Partial:  called(`null`),
				ToolCall: call("toolu_01A9", "read", `null`),
			}),
			want: "not a JSON object",
		},
		"a stop reason the message carries early": {
			seq: func(yield func(llm.Event) bool) {
				msg := streamed()
				msg.StopReason = llm.StopMaxTokens
				yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll", Partial: msg})
			},
			want: "before the stream ended",
		},
		"a completed turn holding a call it never announced": {
			seq: func(yield func(llm.Event) bool) {
				msg := called(`{"path":"auth.go"}`)
				if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: `{"path":"auth.go"}`, Partial: msg}) {
					return
				}
				msg.StopReason = llm.StopToolUse
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
			},
			want: "no EventToolCall announced",
		},
		"a roleless message": {
			seq:  stream(llm.Event{Type: llm.EventMessageStart, Partial: &llm.Message{}}),
			want: "the next time it is replayed",
		},
		"a delta on an event that has no content": {
			seq: stream(llm.Event{
				Type:       llm.EventDone,
				Delta:      "surprise",
				StopReason: llm.StopEndTurn,
				Partial:    &llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopEndTurn},
			}),
			want: "carries Delta",
		},
		"a failure reported as a normal ending": {
			seq: stream(llm.Event{
				Type:       llm.EventError,
				StopReason: llm.StopEndTurn,
				Err:        errors.New("overloaded"),
				Partial:    &llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopEndTurn},
			}),
			want: "error carries one of",
		},
		"a normal ending reported as a failure": {
			seq: stream(llm.Event{
				Type:       llm.EventDone,
				StopReason: llm.StopError,
				Partial:    &llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopError},
			}),
			want: "done carries one of",
		},
		"arguments serialized differently on each side": {
			seq: stream(llm.Event{
				Type:     llm.EventToolCall,
				Partial:  called(`{"path": "auth.go"}`),
				ToolCall: call("toolu_01A9", "read", `{"path":"auth.go"}`),
			}),
			want: "serialized differently",
		},
		"calls announced in an order the message does not hold": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
					{Type: llm.BlockToolUse, ID: "toolu_01", Name: "read", Input: []byte(`{}`)},
					{Type: llm.BlockToolUse, ID: "toolu_02", Name: "read", Input: []byte(`{}`)},
				}}
				for _, id := range []string{"toolu_02", "toolu_01"} {
					ev := llm.Event{
						Type:     llm.EventToolCall,
						Partial:  msg,
						ToolCall: call(id, "read", `{}`),
					}
					if !yield(ev) {
						return
					}
				}
				msg.StopReason = llm.StopToolUse
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
			},
			want: "not the order the message records them in",
		},
		"one call announced twice": {
			seq: func(yield func(llm.Event) bool) {
				msg := called(`{"path":"auth.go"}`)
				for range 2 {
					ev := llm.Event{
						Type:     llm.EventToolCall,
						Partial:  msg,
						ToolCall: call("toolu_01A9", "read", `{"path":"auth.go"}`),
					}
					if !yield(ev) {
						return
					}
				}
				msg.StopReason = llm.StopToolUse
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
			},
			want: "announced tool call \"toolu_01A9\" twice",
		},
		"arguments that are a JSON array": {
			seq: stream(llm.Event{
				Type:     llm.EventToolCall,
				Partial:  called(`[1,2]`),
				ToolCall: call("toolu_01A9", "read", `[1,2]`),
			}),
			want: "not a JSON object",
		},
		"arguments replaced when the call completes": {
			seq: func(yield func(llm.Event) bool) {
				msg := called(`{"path":"auth.go"}`)
				if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: `{"path":"auth.go"}`, Partial: msg}) {
					return
				}
				msg.Content[0].Input = []byte(`{}`)
				yield(llm.Event{
					Type:     llm.EventToolCall,
					Partial:  msg,
					ToolCall: call("toolu_01A9", "read", `{}`),
				})
			},
			want: "does not start with",
		},
		"a role dropped part way through": {
			seq: func(yield func(llm.Event) bool) {
				msg := streamed()
				if !yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll", Partial: msg}) {
					return
				}
				// The pointer survives; the role does not.
				*msg = llm.Message{Content: msg.Content, StopReason: llm.StopEndTurn}
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: msg})
			},
			// Worded so the per-event rule answers, not the settled one.
			want: "the next time it is replayed",
		},
		"a tool_use block with no id": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
					Type: llm.BlockToolUse, Name: "write", Input: []byte(`{"pa`),
				}}}
				if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
					return
				}
				msg.StopReason = llm.StopMaxTokens
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopMaxTokens, Partial: msg})
			},
			want: "has no id",
		},
		"a tool_use block with no name": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
					Type: llm.BlockToolUse, ID: "toolu_01", Input: []byte(`{"pa`),
				}}}
				if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
					return
				}
				msg.StopReason = llm.StopMaxTokens
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopMaxTokens, Partial: msg})
			},
			want: "has no name",
		},
		"two tool_use blocks sharing an id": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
					{Type: llm.BlockToolUse, ID: "toolu_01", Name: "read", Input: []byte(`{}`)},
					{Type: llm.BlockToolUse, ID: "toolu_01", Name: "read", Input: []byte(`{}`)},
				}}
				for range 2 {
					ev := llm.Event{Type: llm.EventToolCall, Partial: msg,
						ToolCall: call("toolu_01", "read", `{}`)}
					if !yield(ev) {
						return
					}
				}
				msg.StopReason = llm.StopToolUse
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
			},
			want: "two tool_use blocks with id",
		},
		"a turn that asks for tools without asking for any": {
			seq: stream(llm.Event{
				Type:       llm.EventDone,
				StopReason: llm.StopToolUse,
				Partial:    &llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopToolUse},
			}),
			want: "stopped to use one",
		},
		"fragments appended to the empty object": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
					Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "read", Input: []byte(`{}`),
				}}}
				if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
					return
				}
				// Appended to the placeholder rather than replacing it.
				msg.Content[0].Input = []byte(`{}{"path":"auth.go"}`)
				yield(llm.Event{Type: llm.EventToolInputDelta, Delta: `{"path":"auth.go"}`, Partial: msg})
			},
			want: "not the delta",
		},
		"calls announced out of order on a truncated turn": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant}
				for _, id := range []string{"toolu_01", "toolu_02"} {
					msg.Content = append(msg.Content, llm.Block{
						Type: llm.BlockToolUse, ID: id, Name: "read", Input: []byte(`{}`),
					})
					if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
						return
					}
				}
				for _, id := range []string{"toolu_02", "toolu_01"} {
					ev := llm.Event{Type: llm.EventToolCall, Partial: msg,
						ToolCall: call(id, "read", `{}`)}
					if !yield(ev) {
						return
					}
				}
				msg.StopReason = llm.StopMaxTokens
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopMaxTokens, Partial: msg})
			},
			want: "not the order the message records them in",
		},
		"arguments changed after the call was announced": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
					{Type: llm.BlockToolUse, ID: "toolu_01", Name: "read", Input: []byte(`{"a":1}`)},
				}}
				if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: `{"a":1}`, Partial: msg}) {
					return
				}
				if !yield(llm.Event{Type: llm.EventToolCall, Partial: msg,
					ToolCall: call("toolu_01", "read", `{"a":1}`)}) {
					return
				}
				// A second call's tail mis-routed onto the first call's block,
				// after the loop was told to run it.
				msg.Content[0].Input = []byte(`{"a":1}{"b":2}`)
				yield(llm.Event{Type: llm.EventToolInputDelta, Delta: `{"b":2}`, Partial: msg})
			},
			want: "after its call was announced",
		},
		"a call renamed after it was announced": {
			seq: func(yield func(llm.Event) bool) {
				msg := called(`{"path":"auth.go"}`)
				if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
					return
				}
				if !yield(llm.Event{Type: llm.EventToolCall, Partial: msg,
					ToolCall: call("toolu_01A9", "read", `{"path":"auth.go"}`)}) {
					return
				}
				msg.Content[0].Name = "write"
				msg.StopReason = llm.StopToolUse
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
			},
			want: "the loop dispatched the first one",
		},
		"a block dropped after the stream ended": {
			seq: func(yield func(llm.Event) bool) {
				msg := streamed()
				if !yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll", Partial: msg}) {
					return
				}
				msg.StopReason = llm.StopEndTurn
				if !yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: msg}) {
					return
				}
				msg.Content = nil
			},
			want: "is gone after the stream ended",
		},
		"an event type this contract does not know": {
			seq: stream(llm.Event{
				Type:    llm.EventType("tool_input_stop"),
				Partial: &llm.Message{Role: llm.RoleAssistant},
			}),
			want: "not an event type this contract knows",
		},
		"a tool_result block in a streamed message": {
			seq: stream(llm.Event{
				Type: llm.EventMessageStart,
				Partial: &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
					Type: llm.BlockToolResult, ToolUseID: "toolu_01", Content: "nope",
				}}},
			}),
			want: "and nothing else",
		},
		"a block retyped mid-stream": {
			seq: func(yield func(llm.Event) bool) {
				msg := streamed()
				if !yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll", Partial: msg}) {
					return
				}
				msg.Content[0].Type = llm.BlockThinking
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: msg})
			},
			want: "is gone", // retyping reads as the block going missing
		},
		"one ToolCall pointer reused for two calls": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant}
				shared := &llm.ToolCall{}
				for _, spec := range []struct{ id, path string }{{"toolu_01", "a.go"}, {"toolu_02", "b.go"}} {
					input := []byte(`{"path":"` + spec.path + `"}`)
					msg.Content = append(msg.Content, llm.Block{
						Type: llm.BlockToolUse, ID: spec.id, Name: "read", Input: input,
					})
					if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
						return
					}
					shared.ID, shared.Name, shared.Input = spec.id, "read", input
					if !yield(llm.Event{Type: llm.EventToolCall, Partial: msg, ToolCall: shared}) {
						return
					}
				}
				msg.StopReason = llm.StopToolUse
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
			},
			want: "each announcement is its own value",
		},
		"a call whose id moved after it was announced": {
			seq: func(yield func(llm.Event) bool) {
				msg := called(`{"path":"auth.go"}`)
				if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
					return
				}
				if !yield(llm.Event{Type: llm.EventToolCall, Partial: msg,
					ToolCall: call("toolu_01A9", "read", `{"path":"auth.go"}`)}) {
					return
				}
				msg.Content[0].ID = "toolu_ZZ"
				msg.StopReason = llm.StopToolUse
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
			},
			want: "the result the loop writes will name the first one",
		},
		"a finished turn holding an empty text block": {
			seq: stream(llm.Event{
				Type:       llm.EventDone,
				StopReason: llm.StopEndTurn,
				Partial: &llm.Message{
					Role:       llm.RoleAssistant,
					StopReason: llm.StopEndTurn,
					Content:    []llm.Block{{Type: llm.BlockText}},
				},
			}),
			want: "an empty text block at index 0",
		},
		"a finished turn with nothing in it": {
			seq: stream(llm.Event{
				Type:       llm.EventDone,
				StopReason: llm.StopEndTurn,
				Partial:    &llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopEndTurn},
			}),
			want: "no blocks in it",
		},
		"a placeholder cleared instead of replaced": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
					Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "read", Input: []byte(`{}`),
				}}}
				if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
					return
				}
				msg.Content[0].Input = nil
				yield(llm.Event{Type: llm.EventToolInputDelta, Partial: msg})
			},
			want: "never rewritten or dropped",
		},
		"one scratch buffer reused for two sets of arguments": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant}
				// The block gets its own copy; the event gets the buffer, which
				// the next call overwrites in place.
				scratch := make([]byte, 0, 64)
				for _, spec := range []struct{ id, path string }{{"toolu_01", "a.go"}, {"toolu_02", "b.go"}} {
					args := `{"path":"` + spec.path + `"}`
					scratch = append(scratch[:0], args...)
					msg.Content = append(msg.Content, llm.Block{
						Type: llm.BlockToolUse, ID: spec.id, Name: "read", Input: []byte(args),
					})
					if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
						return
					}
					ev := llm.Event{Type: llm.EventToolCall, Partial: msg,
						ToolCall: &llm.ToolCall{ID: spec.id, Name: "read", Input: scratch}}
					if !yield(ev) {
						return
					}
				}
				msg.StopReason = llm.StopToolUse
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
			},
			want: "each announcement is its own value",
		},
		"a call renamed mid-stream and put back": {
			seq: func(yield func(llm.Event) bool) {
				msg := called(`{"path":"auth.go"}`)
				if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
					return
				}
				if !yield(llm.Event{Type: llm.EventToolCall, Partial: msg,
					ToolCall: call("toolu_01A9", "read", `{"path":"auth.go"}`)}) {
					return
				}
				// Renamed then restored before the stream ends — invisible to
				// anything that only looks once the events have stopped.
				msg.Content[0].Name = "write"
				if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
					return
				}
				msg.Content[0].Name = "read"
				msg.StopReason = llm.StopToolUse
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
			},
			want: "the loop dispatched the first one",
		},
		"an empty-object fragment with nowhere to land": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
					Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "read",
				}}}
				msg.Content[0].Input = []byte(`{"path":"auth.go"}`)
				if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: `{"path":"auth.go"}`, Partial: msg}) {
					return
				}
				// No block holds the placeholder, so this fragment went nowhere.
				yield(llm.Event{Type: llm.EventToolInputDelta, Delta: `{}`, Partial: msg})
			},
			want: "grew by",
		},
		"two tool_use blocks sharing an id on a truncated turn": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Role: llm.RoleAssistant}
				for _, input := range []string{`{}`, `{"pa`} {
					msg.Content = append(msg.Content, llm.Block{
						Type: llm.BlockToolUse, ID: "toolu_01", Name: "read", Input: []byte(input),
					})
					if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
						return
					}
				}
				msg.StopReason = llm.StopMaxTokens
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopMaxTokens, Partial: msg})
			},
			want: "two tool_use blocks with id",
		},
		"a message_start that does not open the stream": {
			seq: func(yield func(llm.Event) bool) {
				msg := streamed()
				if !yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll", Partial: msg}) {
					return
				}
				yield(llm.Event{Type: llm.EventMessageStart, Partial: msg})
			},
			want: "it is the event that opens a stream",
		},
		"a stop reason on an ordinary event": {
			seq: stream(llm.Event{
				Type:       llm.EventTextDelta,
				Delta:      "I'll",
				Partial:    streamed(),
				StopReason: llm.StopMaxTokens,
			}),
			want: "only the terminal event",
		},
		"a tool call hung off another event": {
			seq: stream(llm.Event{
				Type:     llm.EventMessageStart,
				Partial:  &llm.Message{Role: llm.RoleAssistant},
				ToolCall: &llm.ToolCall{ID: "toolu_01A9", Name: "read"},
			}),
			want: "ToolCall is set on EventToolCall",
		},
		"an error event with no error": {
			seq:  stream(llm.Event{Type: llm.EventError, StopReason: llm.StopError, Partial: &llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopError}}),
			want: "Err is set on EventError",
		},
		"an error hung off a successful event": {
			seq: stream(llm.Event{
				Type:       llm.EventDone,
				StopReason: llm.StopEndTurn,
				Partial:    &llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopEndTurn},
				Err:        errors.New("overloaded"),
			}),
			want: "Err is set on EventError",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := llm.CheckStream(tc.seq)
			mustContain(t, err, tc.want)
		})
	}
}

// TestCheckStreamNoticesChangesAfterTheStream covers the window the agent loop
// persists from, where every rule inside the loop has stopped looking.
func TestCheckStreamNoticesChangesAfterTheStream(t *testing.T) {
	after := func(meddle func(*llm.Message)) llm.StreamResponse {
		return func(yield func(llm.Event) bool) {
			msg := &llm.Message{
				Role:    llm.RoleAssistant,
				Content: []llm.Block{{Type: llm.BlockText, Text: "I'll read it."}},
				Usage:   llm.Usage{Input: 25, Output: 12},
			}
			if !yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll read it.", Partial: msg}) {
				return
			}
			msg.Content = append(msg.Content, llm.Block{
				Type: llm.BlockToolUse, ID: "toolu_01", Name: "read", Input: []byte(`{"path":"a.go"}`),
			})
			if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
				return
			}
			if !yield(llm.Event{Type: llm.EventToolCall, Partial: msg,
				ToolCall: &llm.ToolCall{ID: "toolu_01", Name: "read", Input: []byte(`{"path":"a.go"}`)}}) {
				return
			}
			msg.StopReason = llm.StopToolUse
			if !yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg}) {
				return
			}
			meddle(msg)
		}
	}

	cases := map[string]struct {
		meddle func(*llm.Message)
		want   string
	}{
		"text rewritten": {
			meddle: func(m *llm.Message) { m.Content[0].Text = "something else entirely" },
			want:   "changed after the stream ended",
		},
		"a block appended": {
			meddle: func(m *llm.Message) {
				m.Content = append(m.Content, llm.Block{Type: llm.BlockText, Text: "nobody announced me"})
			},
			want: "appeared at index 2 after the stream ended",
		},
		"the role cleared": {
			meddle: func(m *llm.Message) { m.Role = "" },
			want:   "Partial.Role is \"\" after the stream ended",
		},
		"the stop reason cleared": {
			meddle: func(m *llm.Message) { m.StopReason = "" },
			want:   "the terminal event said",
		},
		"an announced call renamed": {
			meddle: func(m *llm.Message) { m.Content[1].Name = "write" },
			want:   "the loop dispatched the first one",
		},
		"the usage cleared": {
			meddle: func(m *llm.Message) { m.Usage = llm.Usage{} },
			want:   "Usage.Input fell",
		},
		"a tool_result appended": {
			// No channel watches a tool_result, so without the block-type check
			// it lands in the transcript and 400s the next request.
			meddle: func(m *llm.Message) {
				m.Content = append(m.Content, llm.Block{
					Type: llm.BlockToolResult, ToolUseID: "toolu_01", Content: "nope",
				})
			},
			want: "and nothing else",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := llm.CheckStream(after(tc.meddle))
			mustContain(t, err, tc.want)
		})
	}
}

// TestEveryUsageCountIsWatched walks Usage's fields instead of naming them, so a
// count added the day a provider starts reporting one is covered without anyone
// remembering to come back here — Anthropic already splits cache creation into
// 5-minute and 1-hour buckets. Naming them is how a rule ends up with one field
// nothing watches, which is where the next adapter's bug lands.
func TestEveryUsageCountIsWatched(t *testing.T) {
	usage := reflect.TypeFor[llm.Usage]()
	if usage.NumField() == 0 {
		t.Fatal("llm.Usage has no fields, so this test checks nothing at all")
	}

	for i := range usage.NumField() {
		name := usage.Field(i).Name
		t.Run(name, func(t *testing.T) {
			// Every count opens at 10 and this one alone drops, so only a rule
			// reading this field can notice.
			opening := reflect.New(usage).Elem()
			for j := range usage.NumField() {
				// CanSet as well as Kind: an unexported count would pass the kind
				// check and then panic in SetInt with a reflect internals message
				// instead of the one naming the field.
				field := opening.Field(j)
				if kind := field.Kind(); kind != reflect.Int || !field.CanSet() {
					t.Fatalf("Usage.%s is an unsettable %s; the rule compares exported ints",
						usage.Field(j).Name, kind)
				}
				field.SetInt(10)
			}
			dropped := reflect.New(usage).Elem()
			dropped.Set(opening)
			dropped.Field(i).SetInt(9)

			_, err := llm.CheckStream(func(yield func(llm.Event) bool) {
				msg := &llm.Message{
					Role:    llm.RoleAssistant,
					Content: []llm.Block{{Type: llm.BlockText, Text: "I'll read it."}},
					Usage:   opening.Interface().(llm.Usage),
				}
				if !yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll read it.", Partial: msg}) {
					return
				}
				msg.Usage = dropped.Interface().(llm.Usage)
				msg.StopReason = llm.StopEndTurn
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: msg})
			})
			mustContain(t, err, "Usage."+name+" fell from 10 to 9")
		})
	}
}

// TestCheckStreamAcceptsAFailedStream guards the obvious way to write a useless
// contract check: rejecting anything that went wrong.
func TestCheckStreamAcceptsAFailedStream(t *testing.T) {
	msg := &llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopError}
	_, err := llm.CheckStream(stream(llm.Event{
		Type:       llm.EventError,
		StopReason: llm.StopError,
		Partial:    msg,
		Err:        errors.New("overloaded_error"),
	}))
	if err != nil {
		t.Fatalf("CheckStream on a well-formed failure: %v", err)
	}
}

// TestCheckStreamAcceptsRealWireShapes pins what the checker must not reject.
// Each case is what some provider actually sends, so a rule tight enough to
// forbid one would be discovered by an adapter author rather than by us.
func TestCheckStreamAcceptsRealWireShapes(t *testing.T) {
	cases := map[string]llm.StreamResponse{
		// Anthropic: content_block_start carries "input": {}.
		"a no-argument call whose payload arrives at the start": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "list", Input: []byte(`{}`),
			}}}
			if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
				return
			}
			if !yield(llm.Event{
				Type:     llm.EventToolCall,
				Partial:  msg,
				ToolCall: &llm.ToolCall{ID: "toolu_01A9", Name: "list", Input: []byte(`{}`)},
			}) {
				return
			}
			msg.StopReason = llm.StopToolUse
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
		},

		// The allowance is on the placeholder already there, not on the bytes
		// arriving. Both shapes are what the regression this replaces rejected.
		"an empty arguments object arriving as a fragment": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "list",
			}}}
			msg.Content[0].Input = []byte(`{}`)
			if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: `{}`, Partial: msg}) {
				return
			}
			if !yield(llm.Event{Type: llm.EventToolCall, Partial: msg,
				ToolCall: &llm.ToolCall{ID: "toolu_01A9", Name: "list", Input: []byte(`{}`)}}) {
				return
			}
			msg.StopReason = llm.StopToolUse
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
		},
		"an empty arguments object split across fragments": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "list",
			}}}
			for _, fragment := range []string{"{", "}"} {
				msg.Content[0].Input = append(msg.Content[0].Input, fragment...)
				if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: fragment, Partial: msg}) {
					return
				}
			}
			if !yield(llm.Event{Type: llm.EventToolCall, Partial: msg,
				ToolCall: &llm.ToolCall{ID: "toolu_01A9", Name: "list", Input: []byte(`{}`)}}) {
				return
			}
			msg.StopReason = llm.StopToolUse
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
		},

		// Anthropic opens a tool block's fragment stream with an empty
		// partial_json.
		"a delta event whose fragment is empty": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText}}}
			if !yield(llm.Event{Type: llm.EventTextDelta, Partial: msg}) {
				return
			}
			msg.Content[0].Text = "I'll read it."
			if !yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll read it.", Partial: msg}) {
				return
			}
			msg.StopReason = llm.StopEndTurn
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: msg})
		},

		// The Anthropic no-argument shape end to end.
		"a no-argument call that sends the empty object twice": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "list", Input: []byte(`{}`),
			}}}
			if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
				return
			}
			if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: `{}`, Partial: msg}) {
				return
			}
			if !yield(llm.Event{Type: llm.EventToolCall, Partial: msg,
				ToolCall: &llm.ToolCall{ID: "toolu_01A9", Name: "list", Input: []byte(`{}`)}}) {
				return
			}
			msg.StopReason = llm.StopToolUse
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
		},

		// Both blocks sit at `{}` when the second call's first fragment arrives —
		// the shape that broke when this package tracked which block was streamed
		// into.
		"a no-argument call beside one still streaming": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant}
			for _, call := range []struct{ id, name string }{{"toolu_01", "list"}, {"toolu_02", "read"}} {
				msg.Content = append(msg.Content, llm.Block{
					Type: llm.BlockToolUse, ID: call.id, Name: call.name, Input: []byte(`{}`),
				})
				if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
					return
				}
			}
			// The first call's arguments arrive as a fragment while its sibling
			// still sits at the placeholder.
			if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: `{}`, Partial: msg}) {
				return
			}
			if !yield(llm.Event{Type: llm.EventToolCall, Partial: msg,
				ToolCall: &llm.ToolCall{ID: "toolu_01", Name: "list", Input: []byte(`{}`)}}) {
				return
			}
			msg.Content[1].Input = []byte(`{"path":"auth.go"}`)
			if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: `{"path":"auth.go"}`, Partial: msg}) {
				return
			}
			if !yield(llm.Event{Type: llm.EventToolCall, Partial: msg,
				ToolCall: &llm.ToolCall{ID: "toolu_02", Name: "read", Input: msg.Content[1].Input}}) {
				return
			}
			msg.StopReason = llm.StopToolUse
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
		},

		// A short arguments object can arrive whole in the opening chunk.
		"a call whose payload arrives with the block": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "read", Input: []byte(`{"path":"auth.go"}`),
			}}}
			if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
				return
			}
			if !yield(llm.Event{
				Type:     llm.EventToolCall,
				Partial:  msg,
				ToolCall: &llm.ToolCall{ID: "toolu_01A9", Name: "read", Input: msg.Content[0].Input},
			}) {
				return
			}
			msg.StopReason = llm.StopToolUse
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
		},

		// Anthropic again: a faithful adapter accumulates over the empty object.
		"a call whose fragments replace the empty object": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "read", Input: []byte(`{}`),
			}}}
			if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
				return
			}
			if !yield(llm.Event{Type: llm.EventToolInputDelta, Partial: msg}) {
				return
			}
			for i, fragment := range []string{`{"pa`, `th": "auth.go"}`} {
				if i == 0 {
					msg.Content[0].Input = []byte(fragment)
				} else {
					msg.Content[0].Input = append(msg.Content[0].Input, fragment...)
				}
				if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: fragment, Partial: msg}) {
					return
				}
			}
			if !yield(llm.Event{
				Type:     llm.EventToolCall,
				Partial:  msg,
				ToolCall: &llm.ToolCall{ID: "toolu_01A9", Name: "read", Input: msg.Content[0].Input},
			}) {
				return
			}
			msg.StopReason = llm.StopToolUse
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
		},

		// OpenAI-compatible: arguments may not appear until the final chunk.
		"a call whose payload arrives at the end": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "read",
			}}}
			if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
				return
			}
			msg.Content[0].Input = []byte(`{"path":"auth.go"}`)
			if !yield(llm.Event{
				Type:     llm.EventToolCall,
				Partial:  msg,
				ToolCall: &llm.ToolCall{ID: "toolu_01A9", Name: "read", Input: []byte(`{"path":"auth.go"}`)},
			}) {
				return
			}
			msg.StopReason = llm.StopToolUse
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
		},

		// Fragments are indexed by call, so nothing says one call's arguments
		// finish before the next starts.
		"two calls whose arguments interleave": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
				{Type: llm.BlockToolUse, ID: "toolu_01", Name: "read"},
				{Type: llm.BlockToolUse, ID: "toolu_02", Name: "read"},
			}}
			for _, step := range []struct {
				block    int
				fragment string
			}{{0, `{"a`}, {1, `{"b`}, {0, `":1}`}, {1, `":2}`}} {
				msg.Content[step.block].Input = append(msg.Content[step.block].Input, step.fragment...)
				if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: step.fragment, Partial: msg}) {
					return
				}
			}
			for i, id := range []string{"toolu_01", "toolu_02"} {
				ev := llm.Event{Type: llm.EventToolCall, Partial: msg, ToolCall: &llm.ToolCall{
					ID: id, Name: "read", Input: msg.Content[i].Input,
				}}
				if !yield(ev) {
					return
				}
			}
			msg.StopReason = llm.StopToolUse
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
		},

		// The three usage shapes the monotonicity rule was weighed against. The
		// last is the one a rule *requiring* usage would have rejected, and the
		// reason this one only forbids a count going backwards.
		//
		// Anthropic: message_start reports the input counts with output_tokens at
		// 1, and message_delta refines the output count once the reply is done.
		"usage opened at the start and refined at the end": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Usage: llm.Usage{Input: 25, CacheRead: 1024, Output: 1}}
			if !yield(llm.Event{Type: llm.EventMessageStart, Partial: msg}) {
				return
			}
			msg.Content = []llm.Block{{Type: llm.BlockText, Text: "I'll read it."}}
			if !yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll read it.", Partial: msg}) {
				return
			}
			msg.Usage.Output = 15
			msg.StopReason = llm.StopEndTurn
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: msg})
		},

		// OpenAI-compatible with stream_options.include_usage: nothing at all
		// until the final chunk, which is a jump from zero, not a revision.
		"usage that arrives only in the final chunk": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText}}}
			msg.Content[0].Text = "I'll read it."
			if !yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll read it.", Partial: msg}) {
				return
			}
			msg.Usage = llm.Usage{Input: 25, Output: 15}
			msg.StopReason = llm.StopEndTurn
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: msg})
		},

		"a stream that reports no usage at all": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "I'll read it."}}}
			if !yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll read it.", Partial: msg}) {
				return
			}
			msg.StopReason = llm.StopEndTurn
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: msg})
		},

		// Ollama and llama.cpp-style servers report finish_reason "stop" next to
		// tool_calls.
		"tool calls reported alongside a plain ending": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "read", Input: []byte(`{"path":"auth.go"}`),
			}}}
			if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
				return
			}
			if !yield(llm.Event{Type: llm.EventToolCall, Partial: msg,
				ToolCall: &llm.ToolCall{ID: "toolu_01A9", Name: "read", Input: msg.Content[0].Input}}) {
				return
			}
			msg.StopReason = llm.StopEndTurn
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: msg})
		},

		// A 200, a refusal stop reason, no blocks. Committing that is the loop's
		// rule to refuse.
		"a refusal with nothing in it": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopRefusal}
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopRefusal, Partial: msg})
		},

		// Which design §4's termination table says nothing about.
		"a refusal that stops mid-call": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "write", Input: []byte(`{"pa`),
			}}}
			if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: `{"pa`, Partial: msg}) {
				return
			}
			msg.StopReason = llm.StopRefusal
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopRefusal, Partial: msg})
		},

		// Left holding the empty object a provider puts there: indistinguishable
		// from one nothing arrived in, and impossible to have announced.
		"a turn that broke off just after a tool block opened": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: "toolu_01", Name: "read", Input: []byte(`{}`),
			}}}
			if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
				return
			}
			msg.StopReason = llm.StopError
			yield(llm.Event{
				Type:       llm.EventError,
				StopReason: llm.StopError,
				Err:        errors.New("stream ended before message_stop"),
				Partial:    msg,
			})
		},

		// See checkComplete on why holding this turn to an announcement is the
		// more dangerous rule.
		"a call complete but unconfirmed when the stream broke": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: "toolu_01", Name: "write", Input: []byte(`{"path":"a.go"}`),
			}}}
			if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
				return
			}
			msg.StopReason = llm.StopAborted
			yield(llm.Event{
				Type:       llm.EventError,
				StopReason: llm.StopAborted,
				Err:        context.Canceled,
				Partial:    msg,
			})
		},

		// A cancelled turn may end either way — see doneReasons.
		"an abort reported as a normal ending": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopAborted}
			yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopAborted, Partial: msg})
		},
	}

	for name, seq := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := llm.CheckStream(seq); err != nil {
				t.Fatalf("CheckStream: %v", err)
			}
		})
	}
}

// TestCheckStreamMissesAMisroutedFragment pins a hole rather than a rule, which
// is worth a test because design §3.1a now tells a future reader the hole is
// there. An adapter that pours call 2's payload into call 1's block and then
// announces each call from the block it just wrote compares every value against
// itself, so nothing here can see it — the loop would run toolu_1 with b.go's
// arguments and toolu_2 with none.
//
// If this ever starts failing, someone has closed the gap: keep the rule and
// rewrite §3.1a, which currently says closing it belongs to an adapter's own
// tests against a recorded response.
func TestCheckStreamMissesAMisroutedFragment(t *testing.T) {
	_, err := llm.CheckStream(func(yield func(llm.Event) bool) {
		msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockToolUse, ID: "toolu_1", Name: "read", Input: []byte(`{}`)},
			{Type: llm.BlockToolUse, ID: "toolu_2", Name: "read", Input: []byte(`{}`)},
		}}
		if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
			return
		}
		msg.Content[0].Input = []byte(`{"path":"b.go"}`) // call 2's payload, call 1's block
		if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: `{"path":"b.go"}`, Partial: msg}) {
			return
		}
		for i, id := range []string{"toolu_1", "toolu_2"} {
			if !yield(llm.Event{Type: llm.EventToolCall, Partial: msg, ToolCall: &llm.ToolCall{
				ID: id, Name: "read", Input: msg.Content[i].Input,
			}}) {
				return
			}
		}
		msg.StopReason = llm.StopToolUse
		yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopToolUse, Partial: msg})
	})
	if err != nil {
		t.Fatalf("CheckStream now catches a mis-routed fragment (%v); that is an improvement, "+
			"but design §3.1a says it does not, so the spec needs rewriting with it", err)
	}
}

// TestCheckStreamReadsPastAViolation: CheckStream must not be the thing that
// abandons a stream.
func TestCheckStreamReadsPastAViolation(t *testing.T) {
	yielded := 0
	seq := func(yield func(llm.Event) bool) {
		msg := &llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopEndTurn}
		// The first event breaks a rule; the next two are well formed.
		for _, ev := range []llm.Event{
			{Type: llm.EventMessageStart, Delta: "stray", Partial: msg},
			{Type: llm.EventMessageStart, Partial: msg},
			{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: msg},
		} {
			yielded++
			if !yield(ev) {
				return
			}
		}
	}

	events, err := llm.CheckStream(seq)
	mustContain(t, err, "carries Delta")
	if yielded != 3 {
		t.Errorf("provider yielded %d events before being abandoned, want all 3", yielded)
	}
	if len(events) != 3 {
		t.Errorf("CheckStream returned %d events, want all 3", len(events))
	}
}

// TestCheckStreamAcceptsAnUnfinishedCall is the boundary of the rule that every
// tool_use block was announced: rejecting a stream cut off mid-call would fail
// every test of design §4 invariant 2's guard.
func TestCheckStreamAcceptsAnUnfinishedCall(t *testing.T) {
	half := func(stop llm.StopReason, terminal llm.EventType) llm.StreamResponse {
		return func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "write", Input: []byte(`{"pa`),
			}}}
			if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: `{"pa`, Partial: msg}) {
				return
			}
			msg.StopReason = stop
			ev := llm.Event{Type: terminal, StopReason: stop, Partial: msg}
			if terminal == llm.EventError {
				ev.Err = errors.New("stream ended before message_stop")
			}
			yield(ev)
		}
	}

	cases := map[string]llm.StreamResponse{
		"cut off by the output limit": half(llm.StopMaxTokens, llm.EventDone),
		"cut off by a failure":        half(llm.StopError, llm.EventError),
	}
	for name, seq := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := llm.CheckStream(seq); err != nil {
				t.Fatalf("CheckStream on a stream cut off mid-call: %v", err)
			}
		})
	}
}
