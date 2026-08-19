package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/fake"
	"github.com/theocod3s/rasp/internal/tool"
)

// twoCalls is a three-step turn: a tool call, a second tool call, an answer. It
// is rebuilt per case because the sweep below splices a step into it.
func twoCalls() []fake.Step {
	return []fake.Step{
		fake.Text("first, a look"),
		fake.ToolCall("read", `{"path":"a.go"}`),
		fake.Done(llm.StopToolUse),

		fake.Text("now the other"),
		fake.ToolCall("read", `{"path":"b.go"}`),
		fake.Done(llm.StopToolUse),

		fake.Text("both read"),
		fake.Done(llm.StopEndTurn),
	}
}

// TestCancellingAnywhereInAMultiStepTurnLeavesAValidTranscript cancels at every
// point of that turn in turn — before the first token, between a call and the
// stop reason that dispatches it, at each step boundary — and asserts the
// transcript is one the next request can still be built from.
//
// Esc mid-turn is the case that bricks a session (internals §2.4), and it lands
// somewhere different every time it is pressed. A Hook spliced into the script
// cancels at a point the fake decides rather than a clock, so each case is the
// same run every time.
func TestCancellingAnywhereInAMultiStepTurnLeavesAValidTranscript(t *testing.T) {
	var sawCalls, sawSynthetic, deepest int

	for at := range len(twoCalls()) {
		t.Run(fmt.Sprintf("before script step %d", at), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			script := slices.Insert(twoCalls(), at, fake.Hook(cancel))

			a := newAgent(t, agent.Config{
				Provider: fake.New(script...),
				Tools:    registry(&stub{name: "read"}),
			})
			if err := a.Send(ctx, "read both files"); !errors.Is(err, agent.ErrInterrupted) {
				t.Fatalf("Send returned %v; this turn was cancelled part way through", err)
			}

			msgs := a.Messages()
			wantValid(t, msgs)
			deepest = max(deepest, len(msgs))

			for _, msg := range msgs {
				for _, block := range msg.Content {
					switch {
					case block.Type == llm.BlockToolUse:
						sawCalls++
					case block.Type != llm.BlockToolResult:
					case strings.Contains(block.Content, "interrupted"):
						sawSynthetic++
						if !block.IsError {
							t.Errorf("the call the turn never ran was answered with %q and no error "+
								"flag; the model reads the flag to know it has nothing to work from",
								block.Content)
						}
					}
				}
			}
		})
	}

	// Cancelling before the first event leaves a transcript that satisfies every
	// assertion above and exercises none of them, so the sweep says what it found
	// across all of its cases rather than trusting that some case went deep.
	switch {
	case sawCalls == 0:
		t.Error("no case left a tool_use block in the transcript, so the sweep proved nothing about pairing")
	case sawSynthetic == 0:
		t.Error("no case left a call for the loop to answer synthetically; every one of them cancelled " +
			"either side of the window where a call exists and its result does not")
	case deepest < 6:
		t.Errorf("the deepest case reached %d message(s); the script's three steps commit six, so no "+
			"case ran the turn to its last one", deepest)
	}
}

// TestCancellingMidBatchAnswersTheCallsThatNeverRan is design §4's prevent-on-write
// sketch: the batch stops where the cancellation caught it, and the calls behind
// that point are committed already answered rather than left for a later request
// to trip over.
func TestCancellingMidBatchAnswersTheCallsThatNeverRan(t *testing.T) {
	names := []string{"one", "two", "three"}

	for stopAt, stopper := range names {
		t.Run("cancelled by "+stopper, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			stubs := make([]*stub, len(names))
			tools := make([]tool.Tool, len(names))
			for i, name := range names {
				stops := i == stopAt
				stubs[i] = &stub{name: name, run: func(context.Context, json.RawMessage) (tool.Result, error) {
					if stops {
						cancel()
					}
					return tool.Result{Content: name + " ran"}, nil
				}}
				tools[i] = stubs[i]
			}

			// A second turn nobody should reach, so a loop that carried on past the
			// cancellation fails the count below rather than panicking the fake.
			p := fake.New(
				fake.ToolCall("one"), fake.ToolCall("two"), fake.ToolCall("three"),
				fake.Done(llm.StopToolUse),
				fake.Text("all three"), fake.Done(llm.StopEndTurn),
			)
			a := newAgent(t, agent.Config{Provider: p, Tools: registry(tools...)})

			if err := a.Send(ctx, "go"); !errors.Is(err, agent.ErrInterrupted) {
				t.Fatalf("Send returned %v; the turn was cancelled while its batch ran", err)
			}
			if got := len(p.Requests()); got != 1 {
				t.Errorf("the provider was called %d time(s); a cancelled turn takes no further step", got)
			}

			msgs := a.Messages()
			wantPaired(t, msgs, len(names))

			for i, block := range answers(t, msgs) {
				ran := i <= stopAt
				want := 0
				if ran {
					want = 1
				}
				if got := len(stubs[i].calls); got != want {
					t.Errorf("%s ran %d time(s) and should have run %d; the cancellation landed on %s",
						names[i], got, want, stopper)
				}
				switch {
				case ran && (block.IsError || block.Content != names[i]+" ran"):
					t.Errorf("%s ran and was answered with %q (error: %t)", names[i], block.Content, block.IsError)
				case !ran && (!block.IsError || !strings.Contains(block.Content, "interrupted")):
					t.Errorf("%s never ran and was answered with %q (error: %t); a call the user stopped "+
						"says so, because asking again is a suggestion to a person", names[i], block.Content, block.IsError)
				}
			}
		})
	}
}

// TestARepeatedCallHaltsTheTurn is design §4 invariant 3. Nothing supervises the
// model, and a call that returns the same answer to the same arguments is the one
// shape that cannot be progress — so the turn stops on the sixth rather than
// spending the ninety-four steps left in the fuse (internals §4.6).
//
// One more step is scripted than the halt allows, so a loop that failed to notice
// runs out of assertions rather than out of script, and the request count is what
// catches it.
func TestARepeatedCallHaltsTheTurn(t *testing.T) {
	const halts = 6

	var script []fake.Step
	for range halts + 1 {
		script = append(script, fake.ToolCall("read", `{"path":"a.go"}`), fake.Done(llm.StopToolUse))
	}
	script = append(script, fake.Text("done at last"), fake.Done(llm.StopEndTurn))

	p := fake.New(script...)
	var rec recorder
	a := newAgent(t, agent.Config{Provider: p, Tools: registry(&stub{name: "read"}), Events: rec.add})

	err := a.Send(context.Background(), "read it")
	if !errors.Is(err, agent.ErrLooping) {
		t.Fatalf("Send returned %v; a turn making the same call over and over halts", err)
	}
	if got := len(p.Requests()); got != halts {
		t.Errorf("the provider was called %d time(s); the %dth identical call is the one that halts "+
			"the turn, and no step follows it", got, halts)
	}

	// What the user is handed. A halt they cannot read is indistinguishable from
	// the loop giving up on them.
	for _, want := range []string{`"read"`, "6 times", "10 tool calls"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the halt reads %q, and it has to say %s: it is the only account of why the "+
				"turn stopped that reaches a person", err, want)
		}
	}
	if errs := rec.of(agent.EventError); len(errs) != 1 {
		t.Errorf("the halt emitted %d error event(s); it reaches a frontend as an explanation, not "+
			"as a turn that simply stopped", len(errs))
	}
	if ends := rec.of(agent.EventTurnEnd); len(ends) != 1 {
		t.Errorf("the halted turn emitted %d end event(s); every turn ends with exactly one", len(ends))
	}

	// The halt is a step boundary, after the results are committed: a transcript
	// carrying a call nothing answered would brick every request built from it.
	wantPaired(t, a.Messages(), halts)
}

// TestABatchOfIdenticalCallsHaltsWithinOneStep: the window counts tool calls
// rather than steps, so a model that asks for the same thing six times in one
// reply is the same runaway as one that takes six steps over it.
func TestABatchOfIdenticalCallsHaltsWithinOneStep(t *testing.T) {
	const calls = 6

	script := make([]fake.Step, 0, calls+3)
	for range calls {
		script = append(script, fake.ToolCall("read", `{"path":"a.go"}`))
	}
	script = append(script, fake.Done(llm.StopToolUse), fake.Text("done at last"), fake.Done(llm.StopEndTurn))

	p := fake.New(script...)
	a := newAgent(t, agent.Config{Provider: p, Tools: registry(&stub{name: "read"})})

	if err := a.Send(context.Background(), "read it"); !errors.Is(err, agent.ErrLooping) {
		t.Fatalf("Send returned %v; six identical calls in one reply are a loop", err)
	}
	if got := len(p.Requests()); got != 1 {
		t.Errorf("the provider was called %d time(s); the halt lands on the step that made the calls", got)
	}
	wantPaired(t, a.Messages(), calls)
}

// TestARepeatedCallInterleavedWithAnotherStillHalts is why the count is over a
// window rather than a run. Two of one call and one of another, repeating, never
// puts three identical calls in a row — so opencode's simpler rule would let this
// turn run to the fuse, and design §4 takes the windowed version for exactly this
// shape.
func TestARepeatedCallInterleavedWithAnotherStillHalts(t *testing.T) {
	// The eighth call is the sixth `read`, and the window still holds all of them.
	const halts = 8

	var script []fake.Step
	for i := range halts + 1 {
		call := fake.ToolCall("read", `{"path":"a.go"}`)
		if i%3 == 2 {
			call = fake.ToolCall("list", `{"dir":"."}`)
		}
		script = append(script, call, fake.Done(llm.StopToolUse))
	}
	script = append(script, fake.Text("done at last"), fake.Done(llm.StopEndTurn))

	p := fake.New(script...)
	a := newAgent(t, agent.Config{
		Provider: p,
		Tools:    registry(&stub{name: "read"}, &stub{name: "list"}),
	})

	err := a.Send(context.Background(), "read it")
	if !errors.Is(err, agent.ErrLooping) {
		t.Fatalf("Send returned %v; `read` repeated with `list` between it is still a turn going in circles", err)
	}
	if !strings.Contains(err.Error(), `"read"`) {
		t.Errorf("the halt reads %q; the call that repeated is `read`, and naming the other one sends "+
			"the reader after the wrong thing", err)
	}
	if got := len(p.Requests()); got != halts {
		t.Errorf("the provider was called %d time(s); the %dth call is the sixth `read` in a window "+
			"of ten", got, halts)
	}
}

// TestWorkThatDoesNotRepeatItselfRunsToTheEnd holds the other side of the guard,
// which is the one that matters in daily use: a false halt is a turn that
// abandons work the user asked for. Neither case here is a loop, and the reason
// the second is not is that the answer is in the signature.
func TestWorkThatDoesNotRepeatItselfRunsToTheEnd(t *testing.T) {
	// Comfortably past the window, so a guard counting anything coarser than the
	// whole signature has room to fire.
	const steps = 14

	cases := []struct {
		name  string
		input func(i int) string
		tool  func() tool.Tool
	}{
		{
			// Reading fourteen different files is what a turn on an unfamiliar
			// repository looks like.
			name:  "the same tool over different arguments",
			input: func(i int) string { return fmt.Sprintf(`{"path":"%d.go"}`, i) },
			tool:  func() tool.Tool { return &stub{name: "read"} },
		},
		{
			// A test the model is fixing, or a file something else is editing: the
			// call is identical every time and the answer keeps changing, which is
			// progress rather than a loop.
			name:  "the same call answered differently each time",
			input: func(int) string { return `{"cmd":"go test ./..."}` },
			tool: func() tool.Tool {
				runs := 0
				return &stub{name: "bash", run: func(context.Context, json.RawMessage) (tool.Result, error) {
					runs++
					return tool.Result{Content: fmt.Sprintf("%d tests failed", runs)}, nil
				}}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			impl := c.tool()

			var script []fake.Step
			for i := range steps {
				script = append(script, fake.ToolCall(impl.Name(), c.input(i)), fake.Done(llm.StopToolUse))
			}
			script = append(script, fake.Text("all done"), fake.Done(llm.StopEndTurn))

			p := fake.New(script...)
			a := newAgent(t, agent.Config{Provider: p, Tools: registry(impl)})

			if err := a.Send(context.Background(), "get on with it"); err != nil {
				t.Fatalf("Send: %v; this turn repeats no call and has to run to its answer", err)
			}
			if got := len(p.Requests()); got != steps+1 {
				t.Errorf("the provider was called %d time(s); the turn takes %d steps and then answers",
					got, steps+1)
			}
		})
	}
}

// TestAStepIsCommittedWholeOrNotAtAll reads the transcript from inside the step
// that is building it: a tool's Run is on the turn's own goroutine, so it sees
// what anything reading Messages part way through a step sees. The reply holding
// the call it is running must not be there yet — an assistant message published
// before its results is the orphan, whether or not the results land a moment
// later.
func TestAStepIsCommittedWholeOrNotAtAll(t *testing.T) {
	var (
		a       *agent.Agent
		midStep []llm.Message
	)
	second := &stub{name: "second", run: func(context.Context, json.RawMessage) (tool.Result, error) {
		midStep = a.Messages()
		return tool.Result{Content: "ok"}, nil
	}}

	p := fake.New(
		fake.ToolCall("first"), fake.Done(llm.StopToolUse),
		fake.ToolCall("second"), fake.Done(llm.StopToolUse),
		fake.Text("both done"), fake.Done(llm.StopEndTurn),
	)
	a = newAgent(t, agent.Config{Provider: p, Tools: registry(&stub{name: "first"}, second)})

	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(second.calls) != 1 {
		t.Fatalf("the second tool ran %d time(s), so nothing was read part way through a step and this "+
			"test examined an empty transcript", len(second.calls))
	}

	// One call, not two: the second step's reply is still uncommitted while its
	// own tool runs.
	wantPaired(t, midStep, 1)
	if len(midStep) != 3 {
		t.Errorf("the transcript held %d message(s) while the second step ran; the prompt, the first "+
			"reply and its results are three", len(midStep))
	}
	wantPaired(t, a.Messages(), 2)
}
