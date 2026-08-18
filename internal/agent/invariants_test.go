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
