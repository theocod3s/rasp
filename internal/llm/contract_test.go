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

	errType := reflect.TypeFor[error]()
	for i := range method.Type.NumOut() {
		if out := method.Type.Out(i); out.Implements(errType) {
			t.Fatalf("Provider.Stream result %d is %s, which implements error", i, out)
		}
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
// than as some error. A checker that says "invalid stream" for all thirteen of
// these would pass a weaker version of this test and be useless in the one
// moment it matters, which is an adapter author reading its output.
func TestCheckStreamRejects(t *testing.T) {
	// A message that already holds streamed text, for the cases about what
	// happens to it afterwards.
	streamed := func() *llm.Message {
		return &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "I'll"}}}
	}

	cases := map[string]struct {
		seq  llm.StreamResponse
		want string
	}{
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
					msg := &llm.Message{Content: []llm.Block{{Type: llm.BlockText, Text: step.accumulated}}}
					if !yield(llm.Event{Type: llm.EventTextDelta, Delta: step.delta, Partial: msg}) {
						return
					}
				}
			},
			want: "different *Message",
		},
		"a Partial carrying only the delta": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Content: []llm.Block{{Type: llm.BlockText}}}
				for _, chunk := range []string{"I'll", " read"} {
					msg.Content[0].Text = chunk // = , not +=
					if !yield(llm.Event{Type: llm.EventTextDelta, Delta: chunk, Partial: msg}) {
						return
					}
				}
			},
			want: "not the delta",
		},
		"thinking text poured into the text block": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{Content: []llm.Block{{Type: llm.BlockText, Text: "hmm"}}}
				yield(llm.Event{Type: llm.EventThinkingDelta, Delta: "hmm", Partial: msg})
			},
			want: "Partial thinking",
		},
		"accumulated text dropped by a later event": {
			seq: func(yield func(llm.Event) bool) {
				msg := streamed()
				if !yield(llm.Event{Type: llm.EventTextDelta, Delta: "I'll", Partial: msg}) {
					return
				}
				msg.Content = nil
				msg.StopReason = llm.StopEndTurn
				yield(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: msg})
			},
			want: "never rewritten or dropped",
		},
		"an event after the terminal one": {
			seq: func(yield func(llm.Event) bool) {
				msg := &llm.Message{StopReason: llm.StopEndTurn}
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
			seq:  stream(llm.Event{Type: llm.EventDone, Partial: &llm.Message{}}),
			want: "no StopReason",
		},
		"a stop reason the message does not carry": {
			seq:  stream(llm.Event{Type: llm.EventDone, StopReason: llm.StopEndTurn, Partial: &llm.Message{}}),
			want: "Partial.StopReason",
		},
		"a tool call event with no tool call": {
			seq:  stream(llm.Event{Type: llm.EventToolCall, Partial: &llm.Message{}}),
			want: "ToolCall is set on EventToolCall",
		},
		"a tool call hung off another event": {
			seq: stream(llm.Event{
				Type:     llm.EventMessageStart,
				Partial:  &llm.Message{},
				ToolCall: &llm.ToolCall{ID: "toolu_01A9", Name: "read"},
			}),
			want: "ToolCall is set on EventToolCall",
		},
		"an error event with no error": {
			seq:  stream(llm.Event{Type: llm.EventError, StopReason: llm.StopError, Partial: &llm.Message{StopReason: llm.StopError}}),
			want: "Err is set on EventError",
		},
		"an error hung off a successful event": {
			seq: stream(llm.Event{
				Type:       llm.EventDone,
				StopReason: llm.StopEndTurn,
				Partial:    &llm.Message{StopReason: llm.StopEndTurn},
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
