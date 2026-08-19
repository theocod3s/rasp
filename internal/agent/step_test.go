package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/fake"
	"github.com/theocod3s/rasp/internal/tool"
)

// TestTheTurnEndsOnEveryDocumentedStopReason walks design §4's termination
// table. The script holds one turn in every case, so a reason the loop failed to
// terminate on calls the fake a second time and panics there rather than hanging.
func TestTheTurnEndsOnEveryDocumentedStopReason(t *testing.T) {
	boom := errors.New("the gateway gave up")

	cases := []struct {
		name    string
		end     fake.Step
		wantErr error // nil for a turn that finished
	}{
		{"end turn", fake.Done(llm.StopEndTurn), nil},
		{"refusal", fake.Done(llm.StopRefusal), nil},
		{"truncated", fake.Done(llm.StopMaxTokens), nil},
		{"aborted", fake.Done(llm.StopAborted), agent.ErrInterrupted},
		{"failed", fake.Fail(boom), boom},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := fake.New(fake.Text("as far as I got"), c.end)
			var rec recorder
			a := newAgent(t, agent.Config{Provider: p, Events: rec.add})

			err := a.Send(context.Background(), "go")
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("Send returned %v; this reason ends the turn with %v", err, c.wantErr)
			}
			if got := len(p.Requests()); got != 1 {
				t.Errorf("the provider was called %d time(s); the turn ends after one", got)
			}
			if ends := rec.of(agent.EventTurnEnd); len(ends) != 1 {
				t.Errorf("the turn emitted %d end event(s); every turn ends with exactly one", len(ends))
			}
			if errs := rec.of(agent.EventError); (len(errs) == 1) != (c.wantErr != nil) {
				t.Errorf("the turn emitted %d error event(s) and returned %v", len(errs), err)
			}
		})
	}
}

// TestAnInterruptedTurnKeepsWhatArrived is the aborted row: commit what exists,
// end the turn.
func TestAnInterruptedTurnKeepsWhatArrived(t *testing.T) {
	p := fake.New(fake.Text("as far as I got"), fake.Done(llm.StopAborted))
	a := newAgent(t, agent.Config{Provider: p})

	if err := a.Send(context.Background(), "go"); !errors.Is(err, agent.ErrInterrupted) {
		t.Fatalf("Send returned %v; an aborted turn reports it was interrupted", err)
	}
	msgs := a.Messages()
	if len(msgs) != 2 {
		t.Fatalf("the transcript holds %d message(s); the prompt and what the model managed are two", len(msgs))
	}
	if got := msgs[1].Content[0].Text; got != "as far as I got" {
		t.Errorf("the committed reply says %q; the stream delivered %q", got, "as far as I got")
	}
}

// TestAnUnknownStopReasonEndsTheTurnWithAnError is decisions.md's rule: mapping
// the unknown onto "finished" hands the user a stopped turn dressed as a
// complete answer, and providers add reasons between releases.
func TestAnUnknownStopReasonEndsTheTurnWithAnError(t *testing.T) {
	p := &handrolled{reason: "pause_turn", blocks: []llm.Block{{Type: llm.BlockText, Text: "hm"}}}
	a := newAgent(t, agent.Config{Provider: p})

	err := a.Send(context.Background(), "go")
	if err == nil {
		t.Fatal("Send reported a finished turn for a reason nothing maps")
	}
	if !strings.Contains(err.Error(), "pause_turn") {
		t.Errorf("the error is %q, and it has to name the reason nobody has taught the loop", err)
	}
	if p.calls != 1 {
		t.Errorf("the provider was called %d time(s); an unknown reason ends the turn rather than "+
			"looping on it", p.calls)
	}
}

// TestAReplyThatStoppedToUseAToolAndHoldsNoneIsAnError: taking the reason at its
// word and ending the turn would show the user a reply the model did not finish.
func TestAReplyThatStoppedToUseAToolAndHoldsNoneIsAnError(t *testing.T) {
	p := &handrolled{reason: llm.StopToolUse, blocks: []llm.Block{{Type: llm.BlockText, Text: "hm"}}}
	a := newAgent(t, agent.Config{Provider: p})

	err := a.Send(context.Background(), "go")
	if err == nil {
		t.Fatal("Send reported a finished turn for a reply that stopped part way")
	}
	if p.calls != 1 {
		t.Errorf("the provider was called %d time(s)", p.calls)
	}
}

// TestAStreamThatEndsWithoutSayingSoFailsTheTurn: silence is not a completion,
// and a turn built from a reply that may be half there is worse than a failure.
func TestAStreamThatEndsWithoutSayingSoFailsTheTurn(t *testing.T) {
	p := &handrolled{blocks: []llm.Block{{Type: llm.BlockText, Text: "hm"}}, stopEarly: true}
	a := newAgent(t, agent.Config{Provider: p})

	if err := a.Send(context.Background(), "go"); err == nil {
		t.Fatal("Send reported a finished turn for a stream that never said it had ended")
	}
	if msgs := a.Messages(); len(msgs) != 1 {
		t.Errorf("the transcript holds %d message(s); only the prompt belongs there", len(msgs))
	}
}

// handrolled is a provider that does not hold the stream contract. It exists
// because fake.New plays every script through llm.CheckStream and refuses to
// script one of these — so the shapes below are reachable only from an adapter
// that got it wrong, which is what the loop has to survive rather than trust.
type handrolled struct {
	blocks    []llm.Block
	reason    llm.StopReason
	stopEarly bool // end the stream with no terminal event at all
	calls     int
}

func (h *handrolled) ID() string            { return "handrolled" }
func (h *handrolled) Efforts() []llm.Effort { return nil }

func (h *handrolled) Stream(context.Context, llm.Request) llm.StreamResponse {
	h.calls++
	return func(yield func(llm.Event) bool) {
		msg := &llm.Message{Role: llm.RoleAssistant, Content: h.blocks}
		if !yield(llm.Event{Type: llm.EventTextDelta, Partial: msg}) || h.stopEarly {
			return
		}
		msg.StopReason = h.reason
		yield(llm.Event{Type: llm.EventDone, StopReason: h.reason, Partial: msg})
	}
}

// TestATruncatedReplyRunsNoneOfItsCalls is design §4 invariant 2. The guard is on
// the stop reason and on nothing else: arguments cut off at the output limit can
// parse and validate while meaning something other than they say, and a write
// whose content was cut destroys the file it lands in.
func TestATruncatedReplyRunsNoneOfItsCalls(t *testing.T) {
	cases := []struct {
		name  string
		calls []fake.Step
	}{
		// Announced in full, and arguments any schema accepts: nothing below the
		// stop reason has anything to object to, and the call is refused anyway.
		{"arguments that parse", []fake.Step{
			fake.ToolCall("write", `{"path":"a.go","content":"package main"}`),
		}},

		{"arguments cut mid-value", []fake.Step{
			fake.UnfinishedToolCall("write", `{"path":"a.go","content":"packa`),
		}},

		// The limit landed on the second call and the first is as whole as a call
		// ever gets. Both are refused, because the guard is over the message.
		{"one whole call and one cut short", []fake.Step{
			fake.ToolCall("write", `{"path":"a.go","content":"package main"}`),
			fake.UnfinishedToolCall("write", `{"path":"b.go","content":"packa`),
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			write := &stub{name: "write"}
			p := fake.New(append(c.calls,
				fake.Done(llm.StopMaxTokens),

				fake.Text("let me try that again"),
				fake.Done(llm.StopEndTurn),
			)...)

			a := newAgent(t, agent.Config{Provider: p, Tools: registry(write)})
			if err := a.Send(context.Background(), "write it"); err != nil {
				t.Fatalf("Send: %v", err)
			}

			if ran := write.calls(); len(ran) != 0 {
				t.Errorf("write ran %d time(s) from a truncated reply, on %s", len(ran), ran)
			}
			wantPaired(t, a.Messages(), len(c.calls))

			// Read off the request rather than the transcript: what the model is told
			// is the criterion, and a result committed but never sent is silence.
			sent := p.Requests()
			if len(sent) != 2 {
				t.Fatalf("the provider was called %d time(s); the model is told its calls failed and "+
					"takes another step", len(sent))
			}
			for i, block := range answers(t, sent[1].Messages) {
				if !block.IsError || !strings.Contains(block.Content, "output limit") {
					t.Errorf("call %d was answered with %q (error: %t); the model has to read why it was "+
						"refused, or it repeats the call that cannot work", i, block.Content, block.IsError)
				}
				if strings.Contains(block.Content, "interrupted") {
					t.Errorf("call %d was answered with %q; nothing interrupted this turn", i, block.Content)
				}
			}
		})
	}
}

// TestATruncatedReplyWithNothingPendingIsAFinishedTurn is the other half of that
// row: with no calls to fail there is nothing to guard, and the reason travels on
// the committed message for a consumer to warn about.
func TestATruncatedReplyWithNothingPendingIsAFinishedTurn(t *testing.T) {
	p := fake.New(fake.Text("half an ans"), fake.Done(llm.StopMaxTokens))
	a := newAgent(t, agent.Config{Provider: p})

	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v; a reply the cap cut short is a completed turn to the loop", err)
	}
	msgs := a.Messages()
	if got := msgs[len(msgs)-1].StopReason; got != llm.StopMaxTokens {
		t.Errorf("the committed reply stopped for %q; a consumer warns off that field", got)
	}
}

// TestTheFuseStopsARunawayTurn scripts one more turn than the fuse allows, so a
// loop that ignored it would finish successfully and this would fail on the
// error rather than on a panic from the fake.
func TestTheFuseStopsARunawayTurn(t *testing.T) {
	const fuse = 3

	var script []fake.Step
	for range fuse {
		script = append(script, fake.ToolCall("again"), fake.Done(llm.StopToolUse))
	}
	script = append(script, fake.Text("finally"), fake.Done(llm.StopEndTurn))

	p := fake.New(script...)
	a := newAgent(t, agent.Config{Provider: p, Tools: registry(&stub{name: "again"}), MaxSteps: fuse})

	err := a.Send(context.Background(), "go")
	if !errors.Is(err, agent.ErrMaxSteps) {
		t.Fatalf("Send returned %v; a turn that never stops asking for tools hits the fuse", err)
	}
	if got := len(p.Requests()); got != fuse {
		t.Errorf("the provider was called %d time(s) under a fuse of %d", got, fuse)
	}
	wantPaired(t, a.Messages(), fuse)
}

// TestAnOrdinaryTurnIsNowhereNearTheFuse is the other half of "a fuse, not a
// feature": the default has to be high enough that only a turn that stopped
// making progress reaches it.
func TestAnOrdinaryTurnIsNowhereNearTheFuse(t *testing.T) {
	if agent.DefaultMaxSteps != 100 {
		t.Errorf("the default fuse is %d; design §4 sets it at 100", agent.DefaultMaxSteps)
	}

	p := fake.New(
		fake.ToolCall("read"), fake.Done(llm.StopToolUse),
		fake.ToolCall("read"), fake.Done(llm.StopToolUse),
		fake.Text("both read"), fake.Done(llm.StopEndTurn),
	)
	a := newAgent(t, agent.Config{Provider: p, Tools: registry(&stub{name: "read"})})

	if err := a.Send(context.Background(), "read two files"); err != nil {
		t.Fatalf("Send: %v; three steps is an ordinary turn", err)
	}
}

func TestACancelledContextEndsTheTurnBeforeItCallsTheModel(t *testing.T) {
	// A turn the provider would answer perfectly well, so the assertion below is
	// about the loop declining to ask rather than about the stream failing.
	p := fake.New(fake.Text("here you go"), fake.Done(llm.StopEndTurn))
	a := newAgent(t, agent.Config{Provider: p})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := a.Send(ctx, "go")
	if !errors.Is(err, agent.ErrInterrupted) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Send returned %v; a cancelled turn reports both that it was interrupted and why", err)
	}
	if got := len(p.Requests()); got != 0 {
		t.Errorf("the provider was called %d time(s) on an already-cancelled turn", got)
	}
}

// TestCancellingDuringAToolStopsAtTheStepBoundary scripts the step the loop must
// not take, so a loop that took it anyway is caught by the call count rather
// than by running out of script.
func TestCancellingDuringAToolStopsAtTheStepBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	slow := &stub{name: "slow", run: func(context.Context, json.RawMessage) (tool.Result, error) {
		cancel()
		return tool.Result{Content: "ran anyway"}, nil
	}}

	p := fake.New(
		fake.ToolCall("slow"),
		fake.Done(llm.StopToolUse),
		fake.Text("here is what it said"),
		fake.Done(llm.StopEndTurn),
	)
	a := newAgent(t, agent.Config{Provider: p, Tools: registry(slow)})

	err := a.Send(ctx, "go")
	if !errors.Is(err, agent.ErrInterrupted) {
		t.Fatalf("Send returned %v; the turn was cancelled while its tool ran", err)
	}
	if got := len(p.Requests()); got != 1 {
		t.Errorf("the provider was called %d time(s); the cancelled turn takes no further step", got)
	}
	wantPaired(t, a.Messages(), 1)
}

// TestCancellingMidStreamAnswersTheCallItWasMakingIs the case that bricks a
// session if it goes wrong: the reply already holds a tool_use block, and the
// turn ends before anything can run it.
func TestCancellingMidStreamAnswersTheCallItWasMaking(t *testing.T) {
	var cancel context.CancelFunc

	never := &stub{name: "never"}
	p := fake.New(
		fake.ToolCall("never", `{"a":1}`),
		fake.Hook(func() { cancel() }),
		fake.Text("more to come"),
		fake.Done(llm.StopEndTurn),
	)

	var ctx context.Context
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	a := newAgent(t, agent.Config{Provider: p, Tools: registry(never)})
	if err := a.Send(ctx, "go"); !errors.Is(err, agent.ErrInterrupted) {
		t.Fatalf("Send returned %v; the stream was cancelled part way", err)
	}
	if len(never.calls()) != 0 {
		t.Errorf("never ran %d time(s); a cancelled turn dispatches nothing", len(never.calls()))
	}
	msgs := a.Messages()
	wantPaired(t, msgs, 1)
	if result := msgs[2].Content[0]; !result.IsError || !strings.Contains(result.Content, "interrupted") {
		t.Errorf("the unrun call was answered with %q (error: %t); a call the user stopped says so, "+
			"because asking again is a suggestion to a person", result.Content, result.IsError)
	}
}
