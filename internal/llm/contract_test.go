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

// TestStreamHasNoErrorResult is the "never returns a Go error" half of the
// contract, checked over the interface rather than over any implementation.
// Reflection rather than a compile-time assertion is the point: adding an error
// result is a change someone can make for a good local reason, and it would
// break the build at every implementation — which reads as "update the
// adapters", not as "you have just given every call site a second error path to
// forget about". This test says the second thing.
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

// TestEveryEventCarriesTheAccumulatedMessage drives a stream that thinks,
// speaks and calls a tool, and checks the other half of the contract: every
// event carries the whole message so far, as one pointer the provider keeps
// mutating.
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

	// One message, one address. A consumer holding the first event's Partial
	// is holding the same thing the last event handed it, which is what makes
	// "render Partial" the whole of the UI's logic.
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

	// The fragments that made up the arguments do not individually parse; the
	// completed call does, and that is the only version a consumer ever sees.
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
// the one that makes accumulation harder than it looks: the second call's
// arguments start empty while the first call's are already complete, so a
// checker comparing whole channels has to see that as growth rather than as a
// rewrite.
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

	// Both calls are in the message too, in the order they arrived — which is
	// the order their results have to come back in.
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

// TestScriptedFailureArrivesAsATerminalEventError is the ticket's third
// criterion. What it asserts beyond "an error turned up" is the shape of the
// failure: which event type, which stop reason, that the original error is
// still reachable through errors.Is, and that the text streamed before the
// failure is still in the message — a UI that has drawn half a reply must not
// have to erase it.
func TestScriptedFailureArrivesAsATerminalEventError(t *testing.T) {
	overloaded := errors.New("overloaded_error: server is overloaded")
	provider := scripted{id: "test", actions: []action{
		text("Reading the "),
		fail(overloaded, llm.StopError),
		// Never streamed: a failure ends the stream, so the rest of the script
		// is discarded rather than arriving after the terminal event.
		text("rest of it."),
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
	// The retry classifier is a pure function over the Message, so the message
	// has to say the call failed.
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

// TestCancelledTurnArrivesAsATerminalEventError is the same rule for the other
// kind of failure. A cancelled context is not a model error, and it still has
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

// TestConsumerCanStopEarly covers the pull half of "pull-based iterator". A
// consumer that stops reading — the user hits escape, the loop gives up — must
// stop the producer rather than leave it working for nobody. goleak in TestMain
// is the other half of this check: a provider pumping events from a goroutine
// would fail the package, not this test.
//
// Two places matter, hence two cases: the very first yield, and the one inside
// the script. A provider that checks only one of them ignores half the ways a
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
// per rule, each asserting the failure is reported as the rule it broke rather
// than as some error. A checker that answered "invalid stream" to every one of
// these would pass a weaker version of this test and be useless in the one
// moment it matters, which is an adapter author reading its output.
func TestCheckStreamRejects(t *testing.T) {
	// A message that already holds streamed text, for the cases about what
	// happens to it afterwards.
	streamed := func() *llm.Message {
		return &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "I'll"}}}
	}

	// A tool call as it looks once complete, for the cases about the ways the
	// event and the message can disagree about it.
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
				// Both events carry the correct accumulated text, so the only
				// rule broken is the one about the address.
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
			want: "Partial.StopReason",
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
			want: "Partial.Role",
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
			want: "not the order the message records them in",
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
			want: "Partial.Role",
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
				// after the loop has already been told to run it.
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
		"a complete call left unannounced on a failed turn": {
			seq: func(yield func(llm.Event) bool) {
				msg := called(`{"path":"auth.go"}`)
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
			want: "no EventToolCall announced",
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

// TestCheckStreamNoticesAChangeAfterTheStream covers the window the agent loop
// actually persists from: iteration is over, the provider is unwinding, and
// whatever it does to the message now is what gets written down.
func TestCheckStreamNoticesAChangeAfterTheStream(t *testing.T) {
	seq := func(yield func(llm.Event) bool) {
		msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText}}}
		msg.Content[0].Text = "I'll read it."
		if !yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll read it.", Partial: msg}) {
			return
		}
		msg.StopReason = llm.StopEndTurn
		if !yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: msg}) {
			return
		}
		msg.Content[0].Text = "something else entirely"
	}

	_, err := llm.CheckStream(seq)
	mustContain(t, err, "changed after the stream ended")
}

// TestCheckStreamAcceptsAFailedStream guards the obvious way to write a
// contract check that is useless: rejecting anything that went wrong. A stream
// that fails still has to satisfy the contract, and a check that conflates the
// two would fail every retry test in the project.
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

// TestCheckStreamAcceptsRealWireShapes pins three things the checker must not
// reject, because each is what some provider actually sends and a rule tight
// enough to forbid it would be discovered by an adapter author rather than by
// us.
func TestCheckStreamAcceptsRealWireShapes(t *testing.T) {
	cases := map[string]llm.StreamResponse{
		// Anthropic's content_block_start carries "input": {}, so a tool taking
		// no arguments has its whole payload before any delta arrives.
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

		// A no-argument call whose {} arrives as a fragment like any other, and
		// the same object split across two. Both are what the regression this
		// replaces rejected: the allowance is on the placeholder already there,
		// not on the bytes arriving.
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
		// partial_json, so an adapter forwarding its events one-for-one emits a
		// delta that carries nothing. Permitted on purpose: nothing is lost, and
		// a fragment that went missing while Delta said something arrived is
		// caught by the accumulation rule.
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

		// The Anthropic no-argument shape end to end: the empty object opens the
		// block, and the single fragment carries the same two bytes.
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

		// One call's arguments genuinely are the empty object while a sibling's
		// are still streaming. Both blocks sit at `{}` when the first fragment
		// of the second call arrives, which is the shape that broke when this
		// package tried to track which block had been streamed into.
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
			// The first call's arguments are the empty object, arriving as a
			// fragment while its sibling still sits at the placeholder.
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

		// A short arguments object can arrive whole in the chunk that opens the
		// call, so the event that starts a tool block may deliver the payload.
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

		// Anthropic's content_block_start carries "input": {} for every tool_use,
		// so an adapter that copies the field faithfully starts at the empty
		// object and then accumulates fragments over it.
		"a call whose fragments replace the empty object": func(yield func(llm.Event) bool) {
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "read", Input: []byte(`{}`),
			}}}
			if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
				return
			}
			// Anthropic opens the fragment stream with an empty partial_json.
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

		// An OpenAI-compatible endpoint may not reveal arguments until its final
		// chunk, so the whole payload can equally arrive at the completed call.
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

		// Two parallel calls whose fragments interleave, which is what the
		// OpenAI-compatible shape allows: fragments are indexed by call, so
		// nothing says one call's arguments finish before the next one starts.
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

		// Ollama and llama.cpp-style servers report finish_reason "stop" next to
		// tool_calls, so an adapter that maps the reason faithfully ends a turn
		// with calls in it and no claim to have stopped for them.
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

		// A model can decline part way through a tool call, so a refusal is not
		// held to a full set of announcements — design §4's termination table
		// covers a refusal with no tool calls and says nothing about this.
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

		// A cancelled turn is an error to the code that was streaming and a
		// completion to design §4's termination table, so it may end either way.
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

// TestCheckStreamReadsPastAViolation is the other half of the early-stop rule
// CheckStream declines to check. It must not be the thing that abandons a
// stream: a provider that ignores a false yield would die of the runtime's
// range-function panic instead of being told which rule it broke, which loses
// the diagnostic exactly when it is needed.
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
// tool_use block was announced. A stream cut off mid-call — the failure, or the
// output limit — leaves a block no EventToolCall could have announced, because
// the arguments never finished arriving. That is the input design §4 invariant
// 2's guard is written for, so rejecting it here would fail every test of that
// guard before it was written.
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
