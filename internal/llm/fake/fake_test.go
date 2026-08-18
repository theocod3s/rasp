package fake_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/fake"
)

var upstream = errors.New("upstream returned 503")

// ExampleNew is the script design §13 writes, so the shape that document promises
// cannot drift away from the one this package offers.
func ExampleNew() {
	p := fake.New(
		fake.Text("Let me look at that."),
		fake.ToolCall("read", `{"path":"main.go"}`),
		fake.Done(llm.StopToolUse),
	)

	for ev := range p.Stream(context.Background(), llm.Request{Model: "fake-1"}) {
		switch ev.Type {
		case llm.EventToolCall:
			fmt.Printf("run %s%s\n", ev.ToolCall.Name, ev.ToolCall.Input)
		case llm.EventDone:
			fmt.Printf("%q, and then %s\n", ev.Partial.Content[0].Text, ev.StopReason)
		}
	}
	// Output:
	// run read{"path":"main.go"}
	// "Let me look at that.", and then tool_use
}

func TestScriptedTurnsHoldTheStreamContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script []fake.Step
		want   []llm.EventType
		check  func(*testing.T, []llm.Event)
	}{
		{
			name:   "text in chunks is one block",
			script: []fake.Step{fake.Text("Hello, ", "world."), fake.Done(llm.StopEndTurn)},
			want:   []llm.EventType{llm.EventTextDelta, llm.EventTextDelta, llm.EventDone},
			check: func(t *testing.T, events []llm.Event) {
				msg := message(t, events)
				if len(msg.Content) != 1 || msg.Content[0].Text != "Hello, world." {
					t.Errorf("final content is %+v, want one text block reading %q", msg.Content, "Hello, world.")
				}
				if msg.Model != "fake-1" || msg.Provider != fake.ProviderID {
					t.Errorf("message records model %q provider %q, want %q and %q",
						msg.Model, msg.Provider, "fake-1", fake.ProviderID)
				}
			},
		},
		{
			name:   "the optional opener is scripted",
			script: []fake.Step{fake.MessageStart(), fake.Text("hi"), fake.Done(llm.StopEndTurn)},
			want:   []llm.EventType{llm.EventMessageStart, llm.EventTextDelta, llm.EventDone},
		},
		{
			name:   "thinking streams its own block",
			script: []fake.Step{fake.Thinking("weighing ", "it up"), fake.Text("done"), fake.Done(llm.StopEndTurn)},
			want: []llm.EventType{
				llm.EventThinkingDelta, llm.EventThinkingDelta, llm.EventTextDelta, llm.EventDone,
			},
			check: func(t *testing.T, events []llm.Event) {
				msg := message(t, events)
				if len(msg.Content) != 2 || msg.Content[0].Type != llm.BlockThinking || msg.Content[0].Text != "weighing it up" {
					t.Errorf("final content is %+v, want a thinking block then a text block", msg.Content)
				}
			},
		},
		{
			name: "a tool call arrives in fragments that do not parse",
			script: []fake.Step{
				fake.Text("Let me look at that."),
				fake.ToolCall("read", `{"pa`, `th":"main.go"}`),
				fake.Done(llm.StopToolUse),
			},
			want: []llm.EventType{
				llm.EventTextDelta, llm.EventToolInputStart,
				llm.EventToolInputDelta, llm.EventToolInputDelta,
				llm.EventToolCall, llm.EventDone,
			},
			check: func(t *testing.T, events []llm.Event) {
				call := calls(events)
				if len(call) != 1 {
					t.Fatalf("stream announced %d calls, want 1", len(call))
				}
				if call[0].ID != "call_1" || call[0].Name != "read" || string(call[0].Input) != `{"path":"main.go"}` {
					t.Errorf("announced %+v, want call_1 read {\"path\":\"main.go\"}", call[0])
				}
			},
		},
		{
			name:   "a tool taking no arguments still sends an object",
			script: []fake.Step{fake.ToolCall("todos_list"), fake.Done(llm.StopToolUse)},
			want: []llm.EventType{
				llm.EventToolInputStart, llm.EventToolInputDelta, llm.EventToolCall, llm.EventDone,
			},
			check: func(t *testing.T, events []llm.Event) {
				if got := string(calls(events)[0].Input); got != "{}" {
					t.Errorf("announced arguments %q, want %q", got, "{}")
				}
			},
		},
		{
			name: "two calls in one turn are numbered in script order",
			script: []fake.Step{
				fake.ToolCall("read", `{"path":"a.go"}`),
				fake.ToolCall("read", `{"path":"b.go"}`),
				fake.Done(llm.StopToolUse),
			},
			want: []llm.EventType{
				llm.EventToolInputStart, llm.EventToolInputDelta, llm.EventToolCall,
				llm.EventToolInputStart, llm.EventToolInputDelta, llm.EventToolCall,
				llm.EventDone,
			},
			check: func(t *testing.T, events []llm.Event) {
				var ids []string
				for _, call := range calls(events) {
					ids = append(ids, call.ID)
				}
				if !slices.Equal(ids, []string{"call_1", "call_2"}) {
					t.Errorf("announced ids %v, want [call_1 call_2]", ids)
				}
			},
		},
		{
			name: "truncation leaves a fragment nothing announced",
			script: []fake.Step{
				fake.Text("Writing it now."),
				fake.UnfinishedToolCall("write", `{"path":"a.go","content":"pack`),
				fake.Done(llm.StopMaxTokens),
			},
			want: []llm.EventType{
				llm.EventTextDelta, llm.EventToolInputStart, llm.EventToolInputDelta, llm.EventDone,
			},
			check: func(t *testing.T, events []llm.Event) {
				if got := calls(events); len(got) != 0 {
					t.Errorf("stream announced %d calls, want none: the arguments never finished", len(got))
				}
				msg := message(t, events)
				block := msg.Content[len(msg.Content)-1]
				if block.Type != llm.BlockToolUse || string(block.Input) != `{"path":"a.go","content":"pack` {
					t.Errorf("final block is %+v, want a tool_use holding the fragment", block)
				}
				if block.ID == "" || block.Name != "write" {
					t.Errorf("final block is %+v, want an id and the name write", block)
				}
			},
		},
		{
			name:   "truncation before any arguments arrived",
			script: []fake.Step{fake.UnfinishedToolCall("write"), fake.Done(llm.StopMaxTokens)},
			want:   []llm.EventType{llm.EventToolInputStart, llm.EventDone},
		},
		{
			name:   "a refusal is a completed turn",
			script: []fake.Step{fake.Text("I won't do that."), fake.Done(llm.StopRefusal)},
			want:   []llm.EventType{llm.EventTextDelta, llm.EventDone},
		},
		{
			name:   "a failure is a terminal event, not a returned error",
			script: []fake.Step{fake.Text("Calling the API."), fake.Fail(upstream)},
			want:   []llm.EventType{llm.EventTextDelta, llm.EventError},
			check: func(t *testing.T, events []llm.Event) {
				end := last(t, events)
				if !errors.Is(end.Err, upstream) {
					t.Errorf("terminal event carries %v, want %v", end.Err, upstream)
				}
				if end.StopReason != llm.StopError {
					t.Errorf("terminal event reports %q, want %q", end.StopReason, llm.StopError)
				}
			},
		},
		{
			name: "usage rides along on the next event",
			script: []fake.Step{
				fake.Text("cheap"),
				fake.Usage(llm.Usage{Input: 12, Output: 3, CacheRead: 900}),
				fake.Done(llm.StopEndTurn),
			},
			want: []llm.EventType{llm.EventTextDelta, llm.EventDone},
			check: func(t *testing.T, events []llm.Event) {
				want := llm.Usage{Input: 12, Output: 3, CacheRead: 900}
				if got := message(t, events).Usage; got != want {
					t.Errorf("final usage is %+v, want %+v", got, want)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := fake.New(tc.script...)
			events := drain(t, p.Stream(t.Context(), llm.Request{Model: "fake-1"}))

			if got := types(events); !slices.Equal(got, tc.want) {
				t.Errorf("event types %v, want %v", got, tc.want)
			}
			if tc.check != nil {
				tc.check(t, events)
			}
		})
	}
}

// TestEachStreamCallPlaysTheNextTurn is the property the agent loop rests on: one
// flat script, a turn per model call, and nothing shared between them.
func TestEachStreamCallPlaysTheNextTurn(t *testing.T) {
	p := fake.New(
		fake.Text("Reading it."),
		fake.ToolCall("read", `{"path":"main.go"}`),
		fake.Done(llm.StopToolUse),

		fake.ToolCall("bash", `{"cmd":"go test ./..."}`),
		fake.Done(llm.StopToolUse),

		fake.Text("All green."),
		fake.Done(llm.StopEndTurn),
	)

	var messages []*llm.Message
	for turn, want := range [][]llm.EventType{
		{llm.EventTextDelta, llm.EventToolInputStart, llm.EventToolInputDelta, llm.EventToolCall, llm.EventDone},
		{llm.EventToolInputStart, llm.EventToolInputDelta, llm.EventToolCall, llm.EventDone},
		{llm.EventTextDelta, llm.EventDone},
	} {
		events := drain(t, p.Stream(t.Context(), llm.Request{}))
		if got := types(events); !slices.Equal(got, want) {
			t.Errorf("turn %d event types %v, want %v", turn+1, got, want)
		}
		messages = append(messages, message(t, events))
	}

	// Ids keep counting across turns: a transcript holding two calls with one id
	// is one where a tool_result answers both (design §4 invariant 1).
	if got := messages[0].Content[1].ID; got != "call_1" {
		t.Errorf("turn 1 called %q, want call_1", got)
	}
	if got := messages[1].Content[0].ID; got != "call_2" {
		t.Errorf("turn 2 called %q, want call_2", got)
	}

	if messages[0] == messages[1] || messages[1] == messages[2] {
		t.Error("two turns streamed into the same message; each call allocates its own")
	}
	if got := messages[2].StopReason; got != llm.StopEndTurn {
		t.Errorf("last turn stopped with %q, want %q", got, llm.StopEndTurn)
	}
}

func TestStreamPastTheEndOfTheScriptPanics(t *testing.T) {
	p := fake.New(fake.Text("once"), fake.Done(llm.StopEndTurn))
	drain(t, p.Stream(t.Context(), llm.Request{}))

	mustPanic(t, "Stream called 2 time(s), and the script holds 1 turn(s)", func() {
		p.Stream(t.Context(), llm.Request{})
	})
}

// TestAnEmptyScriptRefusesToBeCalled: New() is the assertion that the loop never
// reaches the provider, so it has to be legal to build and loud to use.
func TestAnEmptyScriptRefusesToBeCalled(t *testing.T) {
	p := fake.New()
	mustPanic(t, "the script holds 0 turn(s)", func() {
		p.Stream(t.Context(), llm.Request{})
	})
}

// TestNewRefusesAScriptThatWouldBreakTheContract: the fake is contract-clean for
// whatever a test scripts, not only for the shapes this file exercises. Every
// message here comes from llm.CheckStream rather than from a rule copied out of it.
func TestNewRefusesAScriptThatWouldBreakTheContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script []fake.Step
		want   string
	}{
		{
			name:   "no terminal step",
			script: []fake.Step{fake.Text("and then?")},
			want:   "without a terminal EventDone or EventError",
		},
		{
			name:   "stopped to use a tool it never called",
			script: []fake.Step{fake.Text("here goes"), fake.Done(llm.StopToolUse)},
			want:   "that reason says the model stopped to use one",
		},
		{
			name:   "finished with no content at all",
			script: []fake.Step{fake.Done(llm.StopEndTurn)},
			want:   "no blocks in it is refused by every provider",
		},
		{
			name:   "finished on a call nothing announced",
			script: []fake.Step{fake.UnfinishedToolCall("write", `{"pa`), fake.Done(llm.StopEndTurn)},
			want:   "that no EventToolCall announced",
		},
		{
			name:   "the opener is not the opening event",
			script: []fake.Step{fake.Text("hi"), fake.MessageStart(), fake.Done(llm.StopEndTurn)},
			want:   "message_start arrived after 1 other events",
		},
		{
			name:   "an error reason on a completed turn",
			script: []fake.Step{fake.Text("hi"), fake.Done(llm.StopError)},
			want:   "done carries one of",
		},
		{
			name:   "a failure with nothing to report",
			script: []fake.Step{fake.Text("hi"), fake.Fail(nil)},
			want:   "Err is set on EventError and nothing else",
		},
		{
			name:   "a second turn is checked too",
			script: []fake.Step{fake.Text("hi"), fake.Done(llm.StopEndTurn), fake.Done(llm.StopToolUse)},
			want:   "turn 2 of the script",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustPanic(t, tc.want, func() { fake.New(tc.script...) })
		})
	}
}

func TestAZeroStepIsNotAStep(t *testing.T) {
	mustPanic(t, "not one this package can play", func() {
		fake.New(fake.Step{}, fake.Done(llm.StopEndTurn))
	})
}

// TestHookRunsWhenTheTurnIsPlayed, and not when New checks the script — a hook
// that cancels a context would otherwise fire before the test had begun.
func TestHookRunsWhenTheTurnIsPlayed(t *testing.T) {
	ran := 0
	p := fake.New(fake.Text("a"), fake.Hook(func() { ran++ }), fake.Text("b"), fake.Done(llm.StopEndTurn))
	if ran != 0 {
		t.Fatalf("the hook ran %d time(s) while New was validating the script", ran)
	}

	drain(t, p.Stream(t.Context(), llm.Request{}))
	if ran != 1 {
		t.Errorf("the hook ran %d time(s) over one turn, want 1", ran)
	}
}

func TestCancellationEndsTheTurnAsAnAbortedFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	p := fake.New(
		fake.Text("starting"),
		fake.Hook(cancel),
		fake.Text("this never streams"),
		fake.Done(llm.StopEndTurn),
	)
	events := drain(t, p.Stream(ctx, llm.Request{}))

	want := []llm.EventType{llm.EventTextDelta, llm.EventError}
	if got := types(events); !slices.Equal(got, want) {
		t.Fatalf("event types %v, want %v", got, want)
	}
	end := last(t, events)
	if end.StopReason != llm.StopAborted || !errors.Is(end.Err, context.Canceled) {
		t.Errorf("terminal event reports %q / %v, want %q / %v",
			end.StopReason, end.Err, llm.StopAborted, context.Canceled)
	}
	for _, block := range message(t, events).Content {
		if block.Text == "this never streams" {
			t.Error("a block streamed after the context was cancelled")
		}
	}
}

// TestEveryAbortPointHoldsTheContract cancels one event later each time. A turn
// can be cut anywhere, and what it leaves behind is committed and replayed like
// any other message, so "aborted" is not one shape to check but as many as the
// turn has events.
func TestEveryAbortPointHoldsTheContract(t *testing.T) {
	full := drain(t, fake.New(abortable()...).Stream(t.Context(), llm.Request{}))
	if len(full) < 4 {
		t.Fatalf("the script under test yields %d events, too few to cut anywhere interesting", len(full))
	}

	for at := range len(full) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		p := fake.New(abortable()...)
		events := drain(t, cancelAfter(p.Stream(ctx, llm.Request{}), at, cancel))

		if at == len(full)-1 {
			// Cancelled as the terminal event was handed over: the turn is already
			// finished, so nothing is left to abort.
			if got := types(events); !slices.Equal(got, types(full)) {
				t.Errorf("cancelling at the terminal event gave %v, want the whole turn %v", got, types(full))
			}
			continue
		}
		if got := len(events); got != at+2 {
			t.Errorf("cancelled after event %d and got %d events (%v); want %d — the context is read "+
				"before every event, so the next one is the abort", at, got, types(events), at+2)
		}
		end := last(t, events)
		if end.Type != llm.EventError || end.StopReason != llm.StopAborted {
			t.Errorf("cancelled after event %d and the turn ended with %s / %q, want %s / %q",
				at, end.Type, end.StopReason, llm.EventError, llm.StopAborted)
		}

		// Every block in this script has content, so an empty one is a block the
		// turn opened and never streamed — committed, and refused by the provider
		// the next time the transcript is sent.
		for i, block := range message(t, events).Content {
			if block.Type != llm.BlockToolUse && block.Text == "" {
				t.Errorf("cancelled after event %d and the message holds an empty %s block at index %d",
					at, block.Type, i)
			}
		}
	}
}

// TestAConsumerCanStopEarly: the loop abandons a stream on cancel and on a
// render error, and a provider that yields once more panics the runtime.
func TestAConsumerCanStopEarly(t *testing.T) {
	full := types(drain(t, fake.New(abortable()...).Stream(t.Context(), llm.Request{})))

	for after := 1; after <= len(full); after++ {
		var seen []llm.EventType
		for ev := range fake.New(abortable()...).Stream(t.Context(), llm.Request{}) {
			seen = append(seen, ev.Type)
			if len(seen) == after {
				break
			}
		}
		if !slices.Equal(seen, full[:after]) {
			t.Errorf("stopping after %d events saw %v, want %v", after, seen, full[:after])
		}
	}
}

func TestOneScriptStreamsTheSameEventsTwice(t *testing.T) {
	first := summarize(t, drain(t, fake.New(abortable()...).Stream(t.Context(), llm.Request{Model: "fake-1"})))
	second := summarize(t, drain(t, fake.New(abortable()...).Stream(t.Context(), llm.Request{Model: "fake-1"})))
	if first != second {
		t.Errorf("two runs of one script differ:\n%s\n%s", first, second)
	}
}

func TestRequestsRecordWhatTheCallerSent(t *testing.T) {
	p := fake.New(fake.Text("one"), fake.Done(llm.StopEndTurn), fake.Text("two"), fake.Done(llm.StopEndTurn))

	history := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: "hello"}}}}
	req := llm.Request{
		Model:    "fake-1",
		Messages: history,
		Tools:    []llm.ToolSpec{{Name: "read"}},
		System:   []llm.SystemBlock{{Text: "be brief", Cache: true}},
	}
	drain(t, p.Stream(t.Context(), req))

	// What a caller does with its own slices after the turn: extend the
	// transcript, and go on editing the parts of it that are still live.
	history[0].Content[0].Text = "hello (edited)"
	history[0].Role = llm.RoleAssistant
	req.Tools[0].Name = "write"
	req.System[0].Cache = false
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant})
	drain(t, p.Stream(t.Context(), req))

	got := p.Requests()
	if len(got) != 2 {
		t.Fatalf("recorded %d requests over two turns, want 2", len(got))
	}
	if n := len(got[0].Messages); n != 1 {
		t.Errorf("the first request records %d messages, want the 1 it was sent", n)
	}
	if n := len(got[1].Messages); n != 2 {
		t.Errorf("the second request records %d messages, want 2", n)
	}

	first := got[0]
	switch {
	case first.Messages[0].Content[0].Text != "hello":
		t.Errorf("the first request now reads %q; it was sent %q", first.Messages[0].Content[0].Text, "hello")
	case first.Messages[0].Role != llm.RoleUser:
		t.Errorf("the first request's message is now from %q; it was sent %q", first.Messages[0].Role, llm.RoleUser)
	case first.Tools[0].Name != "read":
		t.Errorf("the first request now offers the tool %q; it was sent %q", first.Tools[0].Name, "read")
	case !first.System[0].Cache:
		t.Error("the first request has lost its cache breakpoint")
	}

	// And the record itself, for the same reason: what a caller does to the slice
	// it was handed must not reach the next reader of it.
	clear(got)
	if len(p.Requests()) != 2 || p.Requests()[0].Model != "fake-1" {
		t.Errorf("Requests() hands out the record itself: %+v", p.Requests())
	}
}

func TestProviderIdentity(t *testing.T) {
	p := fake.New()
	if p.ID() != fake.ProviderID {
		t.Errorf("ID() is %q, want %q", p.ID(), fake.ProviderID)
	}

	efforts := p.Efforts()
	if !slices.Equal(efforts, llm.EffortLadder()) {
		t.Errorf("Efforts() is %v, want the whole ladder %v", efforts, llm.EffortLadder())
	}

	// The contract makes the result the caller's to modify, so a shared slice
	// would leave a picker sorting one in place with a shorter ladder every time.
	clear(efforts)
	if !slices.Equal(p.Efforts(), llm.EffortLadder()) {
		t.Error("Efforts() returns a shared slice; every call owes the caller a fresh one")
	}
}

// abortable is a script with something to interrupt at every kind of boundary:
// between chunks of one block, between blocks, mid-arguments, and after a call
// has been announced.
func abortable() []fake.Step {
	return []fake.Step{
		fake.MessageStart(),
		fake.Text("Reading ", "the file."),
		fake.ToolCall("read", `{"pa`, `th":"main.go"}`),
		fake.Thinking("that looks right"),
		fake.Done(llm.StopToolUse),
	}
}

// cancelAfter passes a stream through, calling cancel just before the event at
// index at. The fake reads the context before producing the next one, so the
// abort lands in a place a test can name.
func cancelAfter(seq llm.StreamResponse, at int, cancel func()) llm.StreamResponse {
	return func(yield func(llm.Event) bool) {
		seen := 0
		for ev := range seq {
			if seen == at {
				cancel()
			}
			seen++
			if !yield(ev) {
				return
			}
		}
	}
}
