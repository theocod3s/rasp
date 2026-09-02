package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/fake"
	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tui/chat"
)

// TestEnterHandsTheTurnToACommandRatherThanRunningIt is design §6 rule 1 at the
// one place it is easiest to lose: Send takes as long as the model takes, and
// calling it from Update would park the UI — no frames, no keys, no interrupt —
// for the whole turn.
func TestEnterHandsTheTurnToACommandRatherThanRunningIt(t *testing.T) {
	// A turner that answers instantly, so a Send called from Update fails the
	// assertion below rather than deadlocking the test into a timeout.
	turner := &promptTurner{started: make(chan context.Context, 1)}
	m := typed(newModel(t.Context(), turner, Config{}), "fix the auth test")

	m, cmd := m.press(key(tea.KeyEnter))

	if cmd == nil {
		t.Fatal("Enter returned no command, so nothing will run the turn")
	}
	select {
	case <-turner.started:
		t.Fatal("Send ran while Update was on the stack; the turn is a tea.Cmd precisely so it does not")
	default:
	}

	// What the user sees for pressing the key, before the model has seen anything.
	if !m.busy {
		t.Error("the model is not busy, so nothing tells the user the turn started")
	}
	if m.input != "" {
		t.Errorf("the input still holds %q after it was sent", m.input)
	}
	if m.cancel == nil {
		t.Error("no cancel func on the model, so an interrupt has nothing to call (design §6 rule 7)")
	}
	if frame := m.View().Content; !strings.Contains(frame, chat.Caret+"fix the auth test") {
		t.Errorf("the frame does not show the prompt that was just sent:\n%s", frame)
	}

	// And the command Bubble Tea would have run does run the turn.
	go cmd()
	select {
	case <-turner.started:
	case <-time.After(settle):
		t.Fatal("the command Enter returned never called Send")
	}
}

// TestEscCancelsTheTurnAndTheUIStaysQuietAboutIt covers three things a
// frontend gets to decide here: Esc is two-stage against a running turn, so
// the first press only arms (design §6 rule 7); the cancel func on the model
// is what the second press reaches; and an interruption is the one failure
// the UI does not draw, because the person who caused it is watching
// (decisions.md).
func TestEscCancelsTheTurnAndTheUIStaysQuietAboutIt(t *testing.T) {
	turner := newTurner(fmt.Errorf("the turn stopped: %w", agent.ErrInterrupted))
	m := typed(newModel(t.Context(), turner, Config{}), "read every file")

	m, cmd := m.press(key(tea.KeyEnter))
	done := run(cmd)
	ctx := started(t, turner.started)

	// cancel closes ctx.Done() synchronously, so a check right after press
	// needs no wait: had the first Esc cancelled outright, the channel would
	// already be closed.
	m, _ = m.press(key(tea.KeyEscape))
	select {
	case <-ctx.Done():
		t.Fatal("the first Esc cancelled the turn outright; it should only arm")
	default:
	}
	if !m.armed {
		t.Fatal("the first Esc against a running turn left the model unarmed")
	}

	m, _ = m.press(key(tea.KeyEscape))
	select {
	case <-ctx.Done():
	case <-time.After(settle):
		t.Fatal("the second Esc left the turn's context live, so nothing below the UI was ever told to stop")
	}

	// Through Update, because that is the only route the outcome takes in a
	// running program: Bubble Tea delivers a command's return value as a message.
	m = update(m, waitFor(t, done))

	if m.busy {
		t.Error("the model is still busy after the turn it was running ended")
	}
	if m.cancel != nil {
		t.Error("the cancel func outlived its turn, so the next Esc cancels something already over")
	}
	if m.err != nil {
		t.Errorf("the UI kept %v to draw; the user pressed the key and knows", m.err)
	}
	// The prompt survives the interruption: a cancelled turn is part of the
	// conversation, not something taken back off the screen.
	if frame := m.View().Content; !strings.Contains(frame, chat.Caret+"read every file") {
		t.Errorf("the interrupted turn's prompt is gone from the frame:\n%s", frame)
	}
}

// TestEscWithNoTurnRunningArmsNothing. Esc's two stages only mean something
// against a turn there is something to cancel; arming one against an idle
// model would confirm on the next press with nothing left to stop.
func TestEscWithNoTurnRunningArmsNothing(t *testing.T) {
	m := newModel(t.Context(), newTurner(nil), Config{})

	m, cmd := m.press(key(tea.KeyEscape))
	if cmd != nil {
		t.Error("Esc with no turn running returned a command; there is nothing for it to do")
	}
	if m.armed {
		t.Error("Esc with no turn running armed anyway, so the next press would confirm nothing")
	}
	if frame := m.View().Content; strings.Contains(frame, "cancel") {
		t.Errorf("the frame hints at a cancel with no turn running to cancel:\n%s", frame)
	}
}

// TestArmedClearsOnAnyOtherKey. The hint only means something for the key
// press right after it; anything else — typing, a backspace, Enter — is the
// user doing something other than confirming the cancel, and the next Esc
// has to start the two stages over rather than land as the second one.
func TestArmedClearsOnAnyOtherKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"a printable key", tea.KeyPressMsg{Code: 'x', Text: "x"}},
		{"backspace", key(tea.KeyBackspace)},
		{"enter", key(tea.KeyEnter)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turner := newTurner(nil)
			m := typed(newModel(t.Context(), turner, Config{}), "go")
			m, cmd := m.press(key(tea.KeyEnter))
			run(cmd)
			started(t, turner.started)

			m, _ = m.press(key(tea.KeyEscape))
			if !m.armed {
				t.Fatal("the first Esc did not arm, so this test is not exercising the clear at all")
			}
			if frame := m.View().Content; !strings.Contains(frame, "cancel") {
				t.Fatalf("arming left no hint on the frame:\n%s", frame)
			}

			m, _ = m.press(tc.key)
			if m.armed {
				t.Errorf("%s did not clear the armed state", tc.name)
			}
			if frame := m.View().Content; strings.Contains(frame, "cancel") {
				t.Errorf("%s left the cancel hint on the frame:\n%s", tc.name, frame)
			}
		})
	}
}

// TestArmedClearsWhenTheTurnEndsOnItsOwn. A turn can end before a second Esc
// ever arrives — it finished, or failed outright — and an arm nothing clears
// then would confirm a cancel on the next Esc with no turn left to stop, and
// would leave the hint on screen for a turn no longer running.
func TestArmedClearsWhenTheTurnEndsOnItsOwn(t *testing.T) {
	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, Config{}), "go")

	m, _ = m.press(key(tea.KeyEnter))
	m, _ = m.press(key(tea.KeyEscape))
	if !m.armed {
		t.Fatal("the first Esc against a running turn left the model unarmed")
	}

	// Driven directly rather than through a real turnDone: what matters here is
	// finish's own bookkeeping, and nothing about how its message arrived.
	m = m.finish(turnDone{})

	if m.armed {
		t.Error("the model is still armed after the turn it was armed against ended on its own")
	}
	if frame := m.View().Content; strings.Contains(frame, "cancel") {
		t.Errorf("the frame still hints at a cancel for a turn that already ended:\n%s", frame)
	}
}

// TestArmedClearsOnEventTurnEndAheadOfFinish. EventTurnEnd and finish's own
// turnDone both say a turn is over, and arrive by separate routes — the bridge
// pump for one, Bubble Tea's command delivery for the other — that make no
// promise about which lands first. An arm cleared only in finish would strand
// the hint on screen for however long the gap to turnDone runs.
func TestArmedClearsOnEventTurnEndAheadOfFinish(t *testing.T) {
	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, Config{}), "go")

	m, _ = m.press(key(tea.KeyEnter))
	m, _ = m.press(key(tea.KeyEscape))
	if !m.armed {
		t.Fatal("the first Esc against a running turn left the model unarmed")
	}

	m = update(m, agentMsg{event: agent.Event{Kind: agent.EventTurnEnd}})

	if m.armed {
		t.Error("EventTurnEnd left the model armed; finish is not the only route a turn ends by")
	}
	if frame := m.View().Content; strings.Contains(frame, "cancel") {
		t.Errorf("the frame still hints at a cancel after EventTurnEnd:\n%s", frame)
	}
}

// TestAFinishedTurnReleasesItsContext. Every turn's context is a child of the
// program's, and a child nobody cancels is held by its parent until the parent
// is cancelled — which here is the end of the session.
func TestAFinishedTurnReleasesItsContext(t *testing.T) {
	turner := &promptTurner{started: make(chan context.Context, 1)}
	m := typed(newModel(t.Context(), turner, Config{}), "hello")

	m, cmd := m.press(key(tea.KeyEnter))
	m = update(m, waitFor(t, run(cmd)))
	ctx := started(t, turner.started)

	select {
	case <-ctx.Done():
	case <-time.After(settle):
		t.Error("the turn ended and its context is still live")
	}
	if m.busy || m.err != nil {
		t.Errorf("a turn that finished left busy=%v and err=%v", m.busy, m.err)
	}
}

// TestAFinishedTurnClosesWithItsDuration reads the closing "took" line off the
// fake clock rather than off how long the test itself took to run: begin
// stamps started when the turn is submitted, EventTurnEnd reads the clock
// again, and the gap between the two is entirely the test's own doing.
func TestAFinishedTurnClosesWithItsDuration(t *testing.T) {
	c := newClock(goldenNow)
	turner := &promptTurner{started: make(chan context.Context, 1)}
	m := newModel(t.Context(), turner, Config{})
	m.now = c.read

	m = typed(m, "go")
	m, _ = m.press(key(tea.KeyEnter))
	c.pass(12 * time.Second)
	m = update(m, agentMsg{event: agent.Event{Kind: agent.EventTurnEnd}})

	if frame := words(m.View().Content); !strings.Contains(frame, "took 12s") {
		t.Errorf("the frame does not close the turn with its duration:\n%s", frame)
	}
}

// TestAFinishedTurnUnderASecondClosesWithNothing mirrors the card and activity
// line's own floor (call.go's elapsed, activity.go's duration): a turn the
// clock says took under a second says nothing about it rather than "took 0s"
// on every turn a synchronous test or a fast reply produces.
func TestAFinishedTurnUnderASecondClosesWithNothing(t *testing.T) {
	c := newClock(goldenNow)
	turner := &promptTurner{started: make(chan context.Context, 1)}
	m := newModel(t.Context(), turner, Config{})
	m.now = c.read

	m = typed(m, "go")
	m, _ = m.press(key(tea.KeyEnter))
	c.pass(400 * time.Millisecond)
	m = update(m, agentMsg{event: agent.Event{Kind: agent.EventTurnEnd}})

	if frame := words(m.View().Content); strings.Contains(frame, "took") {
		t.Errorf("a turn under a second closed with a duration line:\n%s", frame)
	}
}

// TestATurnEndedWithNoBeginBehindItClosesWithNothing covers EventTurnEnd
// reaching a model started never stamped — a question published straight onto
// an idle session (prompt_internal_test.go's asked helper does exactly this).
// Dating that turn's length from the zero time would print a duration in the
// thousands of years rather than the nothing a turn that never really ran
// should close with.
func TestATurnEndedWithNoBeginBehindItClosesWithNothing(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{})

	m = update(m, agentMsg{event: agent.Event{Kind: agent.EventTurnEnd}})

	if frame := words(m.View().Content); strings.Contains(frame, "took") {
		t.Errorf("a turn with no begin behind it closed with a duration line:\n%s", frame)
	}
}

// TestCtrlCCancelsTheTurnBeforeItQuits is Ctrl-C's own two-stage arm (design
// §6 rule 7): the first press cancels a running turn — because the program's
// context would end with the program anyway, but only after Bubble Tea has
// shut the event loop down, and doing it here leaves room to run anything else
// between the interrupt and the exit — and only arms rather than quitting
// outright, so one slipped keystroke costs a turn and not the session. The
// second press, while still armed, is what actually returns the command that
// quits.
func TestCtrlCCancelsTheTurnBeforeItQuits(t *testing.T) {
	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, Config{}), "go")

	m, cmd := m.press(key(tea.KeyEnter))
	run(cmd)
	ctx := started(t, turner.started)

	m, quit := m.press(ctrlCKey)

	select {
	case <-ctx.Done():
	case <-time.After(settle):
		t.Error("the first ctrl+c left the turn's context live, so nothing below the UI was ever told to stop")
	}
	if quit != nil {
		t.Fatal("the first ctrl+c returned the command that quits; it should only arm")
	}
	if !m.quitArmed {
		t.Fatal("the first ctrl+c left the model unarmed")
	}
	if frame := m.View().Content; !strings.Contains(frame, "ctrl+c again to quit") {
		t.Errorf("arming left no hint on the frame:\n%s", frame)
	}

	_, quit = m.press(ctrlCKey)
	if quit == nil {
		t.Fatal("the second ctrl+c returned no command, so the program keeps running")
	}
	if msg := quit(); !isQuit(msg) {
		t.Errorf("the second ctrl+c returned a %T, want the command that quits", msg)
	}
}

// TestOneCtrlCNeverQuitsWithNoTurnRunning: a second press is what guards the
// session itself, not one turn in flight, so the arm has to stand even with
// nothing to cancel — the case escape's own arm deliberately excludes (design
// §6 rule 7, TestEscWithNoTurnRunningArmsNothing).
func TestOneCtrlCNeverQuitsWithNoTurnRunning(t *testing.T) {
	m := newModel(t.Context(), newTurner(nil), Config{})

	m, quit := m.press(ctrlCKey)
	if quit != nil {
		t.Fatal("the first ctrl+c quit with no turn running; it should only arm")
	}
	if !m.quitArmed {
		t.Fatal("the first ctrl+c left the model unarmed")
	}

	_, quit = m.press(ctrlCKey)
	if quit == nil {
		t.Fatal("the second ctrl+c returned no command, so the program keeps running")
	}
	if msg := quit(); !isQuit(msg) {
		t.Errorf("the second ctrl+c returned a %T, want the command that quits", msg)
	}
}

// TestQuitArmedClearsOnAnyOtherKey mirrors TestArmedClearsOnAnyOtherKey for
// Ctrl-C's own arm: the hint only means something for the key right after it,
// so anything else has to start the two stages over rather than land as the
// confirming press.
func TestQuitArmedClearsOnAnyOtherKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"a printable key", tea.KeyPressMsg{Code: 'x', Text: "x"}},
		{"backspace", key(tea.KeyBackspace)},
		{"enter", key(tea.KeyEnter)},
		{"esc", key(tea.KeyEscape)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel(t.Context(), newTurner(nil), Config{})

			m, _ = m.press(ctrlCKey)
			if !m.quitArmed {
				t.Fatal("the first ctrl+c did not arm, so this test is not exercising the clear at all")
			}

			m, _ = m.press(tc.key)
			if m.quitArmed {
				t.Errorf("%s did not clear the ctrl+c arm", tc.name)
			}
			if frame := m.View().Content; strings.Contains(frame, "ctrl+c again to quit") {
				t.Errorf("%s left the quit hint on the frame:\n%s", tc.name, frame)
			}
		})
	}
}

// TestEscThenCtrlCDoesNotEntangleTheTwoArms and its mirror below are the
// acceptance test for the two arms being siblings rather than one state
// wearing two names: each press has to do its own thing and clear the other,
// not confirm across the boundary between them.
func TestEscThenCtrlCDoesNotEntangleTheTwoArms(t *testing.T) {
	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, Config{}), "go")
	m, cmd := m.press(key(tea.KeyEnter))
	run(cmd)
	ctx := started(t, turner.started)

	m, _ = m.press(key(tea.KeyEscape))
	if !m.armed {
		t.Fatal("the first esc against a running turn left the model unarmed")
	}

	// A ctrl+c right after arms its own quit; it must not read as escape's
	// second press and cancel the turn on the strength of an arm it did not set.
	m, quit := m.press(ctrlCKey)
	if m.armed {
		t.Error("ctrl+c left the esc arm standing, so a bare esc next would now cancel")
	}
	if !m.quitArmed {
		t.Error("ctrl+c did not arm its own quit")
	}
	if quit != nil {
		t.Fatal("ctrl+c quit on what should have been its own first press")
	}
	select {
	case <-ctx.Done():
	case <-time.After(settle):
		t.Error("ctrl+c did not cancel the running turn on its own first press")
	}
}

func TestCtrlCThenEscDoesNotEntangleTheTwoArms(t *testing.T) {
	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, Config{}), "go")
	m, cmd := m.press(key(tea.KeyEnter))
	run(cmd)
	started(t, turner.started)

	m, _ = m.press(ctrlCKey)
	if !m.quitArmed {
		t.Fatal("the first ctrl+c left the model unarmed")
	}

	// escape right after arms its own cancel — the turn is still busy, since
	// cancelling only stops its context and finish has not run — and must not
	// read as ctrl+c's second press and quit on the strength of an arm it did
	// not set.
	m, quit := m.press(key(tea.KeyEscape))
	if m.quitArmed {
		t.Error("esc left the ctrl+c arm standing, so a bare ctrl+c next would now quit")
	}
	if !m.armed {
		t.Error("esc did not arm its own cancel")
	}
	if quit != nil {
		t.Fatal("esc returned the command that quits; it does not know how to")
	}
}

// TestASecondPromptWhileATurnRunsIsNotSent. The agent refuses a second Send
// while one is running, and that refusal is an error the user would meet as a
// red line for pressing Enter twice.
func TestASecondPromptWhileATurnRunsIsNotSent(t *testing.T) {
	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, Config{}), "first")

	m, cmd := m.press(key(tea.KeyEnter))
	run(cmd)
	started(t, turner.started)

	m = typed(m, "second")
	sent := m.chat.Len()
	m, second := m.press(key(tea.KeyEnter))

	if second != nil {
		t.Error("a second turn was started while the first was still running")
	}
	if m.chat.Len() != sent {
		t.Errorf("the conversation grew to %d item(s); nothing was sent", m.chat.Len())
	}
	// And the text is still there to send once the turn ends, rather than eaten
	// by a keystroke that did nothing.
	if m.input != "second" {
		t.Errorf("the input holds %q, want what the user typed and could not send yet", m.input)
	}
}

// TestAnInterruptionIsQuietOnEveryRouteItArrivesBy. A cancelled turn reports
// itself twice: as the EventError the loop emits before the turn's end, and as
// Send's own return value. Silencing the second alone still draws it — which is
// how it shipped in a build driven by hand, with the model's interrupt working
// perfectly and the screen reading `error: the turn was interrupted`.
func TestAnInterruptionIsQuietOnEveryRouteItArrivesBy(t *testing.T) {
	stopped := fmt.Errorf("%w: %w", agent.ErrInterrupted, context.Canceled)

	for _, tc := range []struct {
		route string
		msg   tea.Msg
	}{
		{"the loop's error event", agentMsg{event: agent.Event{Kind: agent.EventError, Err: stopped}}},
		{"the turn's own outcome", turnDone{err: stopped}},
	} {
		t.Run(tc.route, func(t *testing.T) {
			m := update(newModel(t.Context(), &promptTurner{}, Config{}), tc.msg)

			if m.err != nil {
				t.Errorf("the UI kept %v to draw, from %s", m.err, tc.route)
			}
			if frame := m.View().Content; strings.Contains(frame, "error") {
				t.Errorf("the frame reports the interruption that arrived by %s:\n%s", tc.route, frame)
			}
		})
	}
}

// TestATurnThatFailedForAnyOtherReasonIsDrawn is the other side of the quiet
// above. Silence is licensed for one error and no other, and a report that
// dropped every error would pass the test before this one on its own — by both
// routes.
func TestATurnThatFailedForAnyOtherReasonIsDrawn(t *testing.T) {
	broke := errors.New("the stream broke")

	for _, tc := range []struct {
		route string
		msg   tea.Msg
	}{
		{"the loop's error event", agentMsg{event: agent.Event{Kind: agent.EventError, Err: broke}}},
		{"the turn's own outcome", turnDone{err: broke}},
	} {
		t.Run(tc.route, func(t *testing.T) {
			m := update(newModel(t.Context(), &promptTurner{}, Config{}), tc.msg)

			if !errors.Is(m.err, broke) {
				t.Errorf("the model kept %v, want the failure that arrived by %s", m.err, tc.route)
			}
			if frame := m.View().Content; !strings.Contains(frame, broke.Error()) {
				t.Errorf("the frame says nothing about the turn failing:\n%s", frame)
			}
		})
	}
}

// TestInterruptingFromTheUILeavesATranscriptTheNextRequestCanBeBuiltFrom drives
// the real loop, cancelled by the real keypress. The invariant itself belongs to
// the agent and is proved there; what this asserts is that the UI reaches it —
// the context Esc cancels is the one the turn is running under, so a turn stopped
// from the keyboard commits its tool_use with a tool_result to match rather than
// abandoning the session mid-pair (design §4 invariant 1).
func TestInterruptingFromTheUILeavesATranscriptTheNextRequestCanBeBuiltFrom(t *testing.T) {
	wait := &blockingTool{entered: make(chan struct{})}

	// The second turn is one nothing should reach: the fake panics when the loop
	// takes a step past the script, which is what a cancellation that failed to
	// stop the loop would do.
	provider := fake.New(
		fake.Text("Reading it now."),
		fake.ToolCall(wait.Name()),
		fake.Done(llm.StopToolUse),

		fake.Text("All done."),
		fake.Done(llm.StopEndTurn),
	)
	a, err := agent.New(agent.Config{
		Provider:  provider,
		Tools:     tool.NewRegistry([]tool.Tool{wait}),
		Model:     "fake-model",
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}

	m := typed(newModel(t.Context(), a, Config{}), "go")
	m, cmd := m.press(key(tea.KeyEnter))
	done := run(cmd)

	select {
	case <-wait.entered:
	case <-time.After(settle):
		t.Fatal("the turn never reached the tool, so there is no in-flight turn to interrupt")
	}
	m, _ = m.press(key(tea.KeyEscape)) // arms
	m, _ = m.press(key(tea.KeyEscape)) // confirms

	outcome := waitFor(t, done)
	if !errors.Is(outcome.err, agent.ErrInterrupted) {
		t.Fatalf("the turn reported %v, want the interruption Esc caused (decisions.md)", outcome.err)
	}

	msgs := a.Messages()
	if len(msgs) != 3 {
		t.Fatalf("the transcript holds %d message(s); the prompt, the reply that asked for a tool, "+
			"and the result answering it are all three of them: %+v", len(msgs), msgs)
	}
	asked := blockIDs(msgs[1], llm.BlockToolUse)
	answered := blockIDs(msgs[2], llm.BlockToolResult)
	if len(asked) == 0 {
		t.Fatal("the turn was interrupted before it asked for a tool, so the pairing below checks nothing")
	}
	if !slices.Equal(asked, answered) {
		t.Errorf("the transcript asks for tool_use %v and answers %v; every provider rejects a request "+
			"built from a transcript where those differ", asked, answered)
	}
}

func blockIDs(msg llm.Message, kind llm.BlockType) []string {
	var out []string
	for _, block := range msg.Content {
		switch {
		case block.Type != kind:
		case kind == llm.BlockToolUse:
			out = append(out, block.ID)
		default:
			out = append(out, block.ToolUseID)
		}
	}
	return out
}

// blockingTool holds the turn open until its context is cancelled, which is what
// gives a test somewhere to press Esc that is not a clock.
type blockingTool struct{ entered chan struct{} }

func (b *blockingTool) Name() string           { return "wait" }
func (b *blockingTool) Description() string    { return "blocks until the turn is cancelled" }
func (b *blockingTool) Schema() map[string]any { return map[string]any{"type": "object"} }

func (b *blockingTool) Run(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
	close(b.entered)
	<-ctx.Done()
	return tool.Result{}, ctx.Err()
}

// turner is a Turner a test drives by hand: it publishes the context its turn
// runs under and stays in Send until that context is cancelled.
type turner struct {
	started chan context.Context
	err     error
}

func newTurner(err error) *turner {
	return &turner{started: make(chan context.Context, 1), err: err}
}

func (t *turner) Send(ctx context.Context, _ string) error {
	t.started <- ctx
	<-ctx.Done()
	return t.err
}

// promptTurner answers instantly, which is the turn nobody interrupts.
type promptTurner struct{ started chan context.Context }

func (p *promptTurner) Send(ctx context.Context, _ string) error {
	p.started <- ctx
	return nil
}

func started(t *testing.T, ch chan context.Context) context.Context {
	t.Helper()
	select {
	case ctx := <-ch:
		return ctx
	case <-time.After(settle):
		t.Fatal("the turn never started")
		return nil
	}
}

// update drives one message through the real entry point, so a case Update does
// not route is a failure here rather than an untested method call.
func update(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

// run puts a command on a goroutine of its own, which is what Bubble Tea does
// with one (design §6, the [turn] row).
func run(cmd tea.Cmd) chan tea.Msg {
	out := make(chan tea.Msg, 1)
	go func() { out <- cmd() }()
	return out
}

func waitFor(t *testing.T, out chan tea.Msg) turnDone {
	t.Helper()
	select {
	case msg := <-out:
		done, ok := msg.(turnDone)
		if !ok {
			t.Fatalf("the turn's command returned %T, and the loop only knows what to do with a turnDone", msg)
		}
		return done
	case <-time.After(settle):
		t.Fatal("the turn's command never returned")
		return turnDone{}
	}
}

func typed(m Model, text string) Model {
	for _, r := range text {
		m, _ = m.press(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

func key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

var ctrlCKey = tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'c'}

func isQuit(msg tea.Msg) bool {
	_, ok := msg.(tea.QuitMsg)
	return ok
}
