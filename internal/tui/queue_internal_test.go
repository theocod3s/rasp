package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/fake"
	"github.com/theocod3s/rasp/internal/tool"
)

// TestTypingDuringATurnComposesALineAndQueuesIt. The keyboard is not taken away
// while the model works: the draft grows under the running turn, and Enter puts
// it in the queue rather than throwing it away.
func TestTypingDuringATurnComposesALineAndQueuesIt(t *testing.T) {
	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, Config{}), "first")
	m, cmd := m.press(key(tea.KeyEnter))
	run(cmd)
	started(t, turner.started)

	m = typed(m, "also update the README")
	if m.input.text != "also update the README" {
		t.Fatalf("the input holds %q while a turn runs; typing is not reaching the draft", m.input.text)
	}
	if frame := words(m.View().Content); !strings.Contains(frame, "also update the README") {
		t.Errorf("the frame does not show what is being typed mid-turn:\n%s", frame)
	}

	m, second := m.press(key(tea.KeyEnter))
	if second != nil {
		t.Error("Enter mid-turn returned a command; the agent runs one turn at a time")
	}
	if !slices.Equal(m.queue, []string{"also update the README"}) {
		t.Errorf("the queue holds %q after Enter mid-turn", m.queue)
	}
}

// TestQueuedMessagesSendInOrderWhenTheTurnEnds drives the real agent rather
// than a fake Turner, because the failure this guards against is one only the
// agent can produce: a second Send reaching a turn that has not released yet is
// answered with ErrTurnInProgress, and the queued message would be lost to an
// error line instead of reaching the model.
func TestQueuedMessagesSendInOrderWhenTheTurnEnds(t *testing.T) {
	provider := fake.New(
		fake.Text("one."), fake.Done(llm.StopEndTurn),
		fake.Text("two."), fake.Done(llm.StopEndTurn),
		fake.Text("three."), fake.Done(llm.StopEndTurn),
	)
	a, err := agent.New(agent.Config{
		Provider:  provider,
		Tools:     tool.NewRegistry(nil),
		Model:     "fake-model",
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}

	m := typed(newModel(t.Context(), a, Config{}), "first")
	m, cmd := m.press(key(tea.KeyEnter))

	m = typed(m, "second")
	m, _ = m.press(key(tea.KeyEnter))
	m = typed(m, "third")
	m, _ = m.press(key(tea.KeyEnter))
	if !slices.Equal(m.queue, []string{"second", "third"}) {
		t.Fatalf("the queue holds %q before the first turn ended", m.queue)
	}

	// Each turn's own turnDone is what drains the next, so the chain is walked
	// the way Bubble Tea walks it: run the command, hand its message back.
	for range 3 {
		if cmd == nil {
			t.Fatal("the turn returned no command, so nothing after it will run")
		}
		m, cmd = m.finish(waitFor(t, run(cmd)))
	}

	if len(m.queue) != 0 {
		t.Errorf("the queue still holds %q after every turn ended", m.queue)
	}
	// Three requests rather than one: a drain that reached the agent too early is
	// refused with ErrTurnInProgress, which never reaches the provider at all.
	sent := provider.Requests()
	if len(sent) != 3 {
		t.Fatalf("the provider saw %d request(s); each queued message is its own turn", len(sent))
	}
	if want := []string{"first", "second", "third"}; !slices.Equal(prompts(sent[2]), want) {
		t.Errorf("the last request carries the prompts %q, want %q", prompts(sent[2]), want)
	}
	if frame := words(m.View().Content); !strings.Contains(frame, "third") {
		t.Errorf("the drained message never reached the conversation:\n%s", frame)
	}
}

// prompts is every user message in a request, in transcript order.
func prompts(req llm.Request) []string {
	var out []string
	for _, msg := range req.Messages {
		if msg.Role != llm.RoleUser {
			continue
		}
		for _, block := range msg.Content {
			if block.Type == llm.BlockText {
				out = append(out, block.Text)
			}
		}
	}
	return out
}

// TestTheQueueDrainsOnTurnDoneAndNotOnEventTurnEnd is the single-drain proof.
// A turn ends by two routes that make no promise about which lands first, and
// only one of them means the agent has returned from Send — so EventTurnEnd
// must move nothing out of the queue, and the turnDone behind it must move
// exactly one message however many EventTurnEnds preceded it.
func TestTheQueueDrainsOnTurnDoneAndNotOnEventTurnEnd(t *testing.T) {
	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, Config{}), "first")
	m, cmd := m.press(key(tea.KeyEnter))
	run(cmd)
	started(t, turner.started)

	m = typed(m, "second")
	m, _ = m.press(key(tea.KeyEnter))
	m = typed(m, "third")
	m, _ = m.press(key(tea.KeyEnter))

	m = update(m, agentMsg{event: agent.Event{Kind: agent.EventTurnEnd}})
	if !slices.Equal(m.queue, []string{"second", "third"}) {
		t.Fatalf("EventTurnEnd drained the queue down to %q; the agent is still inside Send at that "+
			"point and a message sent now meets ErrTurnInProgress", m.queue)
	}
	// And a second one changes nothing either: the queue's trigger is not this
	// event however many times it arrives.
	m = update(m, agentMsg{event: agent.Event{Kind: agent.EventTurnEnd}})
	if !slices.Equal(m.queue, []string{"second", "third"}) {
		t.Fatalf("a repeated EventTurnEnd drained the queue down to %q", m.queue)
	}

	m = update(m, turnDone{})
	if !slices.Equal(m.queue, []string{"third"}) {
		t.Errorf("turnDone left the queue at %q, want exactly one message drained", m.queue)
	}
}

// TestTypeAheadIsQueuedInTheGapBetweenTheTwoEndSignals is the other half of the
// race the drain avoids, seen from the keyboard: EventTurnEnd puts busy down
// while Send has not yet returned, and a prompt sent into that window would
// reach an agent that still holds its turn. Enter has to queue there too.
func TestTypeAheadIsQueuedInTheGapBetweenTheTwoEndSignals(t *testing.T) {
	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, Config{}), "first")
	m, cmd := m.press(key(tea.KeyEnter))
	run(cmd)
	started(t, turner.started)

	m = update(m, agentMsg{event: agent.Event{Kind: agent.EventTurnEnd}})
	if m.busy {
		t.Fatal("EventTurnEnd left the model busy, so this test is not exercising the gap at all")
	}

	m = typed(m, "second")
	m, second := m.press(key(tea.KeyEnter))

	if second != nil {
		t.Error("Enter started a turn between EventTurnEnd and turnDone, where the agent has not " +
			"returned from Send yet")
	}
	if !slices.Equal(m.queue, []string{"second"}) {
		t.Errorf("the queue holds %q; the line typed into the gap was not kept", m.queue)
	}
}

// TestAQueueSurvivesATurnThatFailedOrWasCancelled. The queued messages were
// composed against a conversation that did not happen, so nothing is sent into
// it — and nothing is dropped either. The user is told, and the queue stays on
// screen where they can act on it.
func TestAQueueSurvivesATurnThatFailedOrWasCancelled(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"a failed turn", errors.New("the provider closed the stream mid-message")},
		{"an interrupted turn", agent.ErrInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turner := newTurner(tc.err)
			m := typed(newModel(t.Context(), turner, Config{}), "first")
			m, cmd := m.press(key(tea.KeyEnter))
			run(cmd)
			started(t, turner.started)

			m = typed(m, "second")
			m, _ = m.press(key(tea.KeyEnter))

			m, drained := m.finish(turnDone{err: tc.err})

			if drained != nil {
				t.Error("the queue drained into a turn that had just gone wrong")
			}
			if !slices.Equal(m.queue, []string{"second"}) {
				t.Errorf("the queue holds %q after the turn ended badly; nothing may be lost here", m.queue)
			}
			frame := words(m.View().Content)
			if !strings.Contains(frame, "was not sent") {
				t.Errorf("the frame never says the queue was held:\n%s", frame)
			}
			if !strings.Contains(frame, "1 queued") {
				t.Errorf("the held queue is not visible on the frame:\n%s", frame)
			}
		})
	}
}

// TestAHeldQueueDrainsBehindTheNextTurnThatFinishes is what "the user decides"
// resolves to: the hold is on the turn that went wrong, not on the queue for
// good, and the next turn — one only a keystroke can start — carries it out.
func TestAHeldQueueDrainsBehindTheNextTurnThatFinishes(t *testing.T) {
	turner := newTurner(errors.New("the stream broke"))
	m := typed(newModel(t.Context(), turner, Config{}), "first")
	m, cmd := m.press(key(tea.KeyEnter))
	run(cmd)
	started(t, turner.started)

	m = typed(m, "second")
	m, _ = m.press(key(tea.KeyEnter))
	m, _ = m.finish(turnDone{err: turner.err})
	if len(m.queue) != 1 {
		t.Fatalf("the queue holds %q after the failed turn, so this test starts from the wrong state", m.queue)
	}

	m = typed(m, "carry on")
	m, cmd = m.press(key(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("Enter on an idle model with a held queue started nothing")
	}
	m, drained := m.finish(turnDone{})

	if drained == nil {
		t.Error("a turn that finished cleanly did not drain the held queue")
	}
	if len(m.queue) != 0 {
		t.Errorf("the queue still holds %q behind a turn that finished cleanly", m.queue)
	}
}

// TestUpRecallsTheHeadOfTheQueueIntoTheInput, and only where Up has nothing
// else to do: the binding must not cost the caret a movement it can already
// make inside a draft of several lines.
func TestUpRecallsTheHeadOfTheQueueIntoTheInput(t *testing.T) {
	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, Config{}), "first")
	m, cmd := m.press(key(tea.KeyEnter))
	run(cmd)
	started(t, turner.started)

	m = typed(m, "second")
	m, _ = m.press(key(tea.KeyEnter))
	m = typed(m, "third")
	m, _ = m.press(key(tea.KeyEnter))

	m, _ = m.press(key(tea.KeyUp))

	if m.input.text != "second" {
		t.Errorf("the input holds %q, want the head of the queue back for editing", m.input.text)
	}
	if m.input.at != len("second") {
		t.Errorf("the caret sits at %d, want the end of the recalled line", m.input.at)
	}
	if !slices.Equal(m.queue, []string{"third"}) {
		t.Errorf("the queue holds %q; the recalled message must leave it", m.queue)
	}

	// A second Up is the caret's own again: the draft is no longer empty, so
	// there is a line to move around in.
	m, _ = m.press(key(tea.KeyUp))
	if m.input.text != "second" {
		t.Errorf("a second Up changed the draft to %q", m.input.text)
	}
	if !slices.Equal(m.queue, []string{"third"}) {
		t.Errorf("a second Up recalled again, leaving %q", m.queue)
	}
}

// TestUpInsideADraftStillMovesTheCaret is the negative control for the binding
// above. A recall that fired on any Up would take the up-arrow away from every
// draft of more than one line, which is the whole of how the caret reaches the
// line above.
func TestUpInsideADraftStillMovesTheCaret(t *testing.T) {
	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, Config{}), "first")
	m, cmd := m.press(key(tea.KeyEnter))
	run(cmd)
	started(t, turner.started)

	m = typed(m, "queued")
	m, _ = m.press(key(tea.KeyEnter))

	m = typed(m, "top")
	m, _ = m.press(key(tea.KeyTab)) // breaks the line
	m = typed(m, "bottom")
	m, _ = m.press(key(tea.KeyUp))

	if m.input.text != "top\nbottom" {
		t.Errorf("the draft is now %q; Up took the line the user was writing", m.input.text)
	}
	if m.input.at != len("top") {
		t.Errorf("the caret sits at %d, want the end of the line above", m.input.at)
	}
	if !slices.Equal(m.queue, []string{"queued"}) {
		t.Errorf("Up inside a draft recalled from the queue, leaving %q", m.queue)
	}
}

// TestARecalledMessageEmptiedIsDiscarded. Nothing sends an empty draft, so
// backspacing a recalled line to nothing is the only way to drop a queued
// message — and it has to actually drop it rather than leave a copy behind.
func TestARecalledMessageEmptiedIsDiscarded(t *testing.T) {
	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, Config{}), "first")
	m, cmd := m.press(key(tea.KeyEnter))
	run(cmd)
	started(t, turner.started)

	m = typed(m, "oops")
	m, _ = m.press(key(tea.KeyEnter))
	m, _ = m.press(key(tea.KeyUp))
	for range len("oops") {
		m, _ = m.press(key(tea.KeyBackspace))
	}
	m, sent := m.press(key(tea.KeyEnter))

	if sent != nil {
		t.Error("Enter on an emptied draft started something")
	}
	if len(m.queue) != 0 {
		t.Errorf("the queue still holds %q after the recalled message was emptied", m.queue)
	}
	m, _ = m.finish(turnDone{})
	if frame := words(m.View().Content); strings.Contains(frame, "oops") {
		t.Errorf("the discarded message was sent anyway:\n%s", frame)
	}
}

// TestASlashCommandMidTurnIsAnsweredRatherThanQueued. A command is not a
// message: queueing one would leave the user waiting for an answer that arrives
// a turn later as a prompt the model reads out loud.
func TestASlashCommandMidTurnIsAnsweredRatherThanQueued(t *testing.T) {
	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, Config{}), "first")
	m, cmd := m.press(key(tea.KeyEnter))
	run(cmd)
	started(t, turner.started)

	for _, tc := range []struct {
		line string
		says string
	}{
		{"/clear", "needs the running turn to be over first"},
		{"/nope", "There is no /nope command"},
		{"/resume", "needs the session store"},
	} {
		before := m
		m = typed(m, tc.line)
		m, _ = m.press(key(tea.KeyEnter))

		if len(m.queue) != 0 {
			t.Errorf("%s was queued, leaving %q", tc.line, m.queue)
		}
		if frame := words(m.View().Content); !strings.Contains(frame, tc.says) {
			t.Errorf("%s drew no answer:\n%s", tc.line, frame)
		}
		if m.chat.Len() != before.chat.Len()+1 {
			t.Errorf("%s added %d items to the conversation, want its one answer",
				tc.line, m.chat.Len()-before.chat.Len())
		}
	}
}

// TestTheArmsBehaveTheSameWithAQueueStanding. Esc's two stages and Ctrl-C's are
// about the turn and the session (design §6 rule 7); a queue behind them is not
// part of either question, and neither key may start reading it.
func TestTheArmsBehaveTheSameWithAQueueStanding(t *testing.T) {
	queued := func(t *testing.T) (Model, *turner, context.Context) {
		t.Helper()
		turner := newTurner(agent.ErrInterrupted)
		m := typed(newModel(t.Context(), turner, Config{}), "first")
		m, cmd := m.press(key(tea.KeyEnter))
		run(cmd)
		ctx := started(t, turner.started)

		m = typed(m, "second")
		m, _ = m.press(key(tea.KeyEnter))
		return m, turner, ctx
	}

	t.Run("esc esc still cancels and leaves the queue", func(t *testing.T) {
		m, _, ctx := queued(t)

		m, _ = m.press(key(tea.KeyEscape))
		if !m.armed {
			t.Fatal("the first Esc against a running turn left the model unarmed")
		}
		select {
		case <-ctx.Done():
			t.Fatal("the first Esc cancelled the turn outright; it should only arm")
		default:
		}

		m, _ = m.press(key(tea.KeyEscape))
		select {
		case <-ctx.Done():
		case <-time.After(settle):
			t.Fatal("the second Esc left the turn's context live")
		}
		if !slices.Equal(m.queue, []string{"second"}) {
			t.Errorf("cancelling the turn changed the queue to %q", m.queue)
		}
	})

	t.Run("ctrl+c still arms rather than quitting", func(t *testing.T) {
		m, _, ctx := queued(t)

		m, quit := m.press(ctrlCKey)
		if quit != nil {
			t.Fatal("the first ctrl+c returned the command that quits; it should only arm")
		}
		if !m.quitArmed {
			t.Fatal("the first ctrl+c left the model unarmed")
		}
		select {
		case <-ctx.Done():
		case <-time.After(settle):
			t.Error("the first ctrl+c did not cancel the running turn")
		}
		if !slices.Equal(m.queue, []string{"second"}) {
			t.Errorf("ctrl+c changed the queue to %q", m.queue)
		}

		_, quit = m.press(ctrlCKey)
		if quit == nil {
			t.Fatal("the second ctrl+c returned no command, so the program keeps running")
		}
		if msg := quit(); !isQuit(msg) {
			t.Errorf("the second ctrl+c returned a %T, want the command that quits", msg)
		}
	})
}

// TestADrainedTurnDoesNotEatTheDraftBeingComposed. The queue and the input line
// are different places, and the message that drains is taken from the first —
// so whatever the user is halfway through typing when the turn ends is still
// theirs.
func TestADrainedTurnDoesNotEatTheDraftBeingComposed(t *testing.T) {
	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, Config{}), "first")
	m, cmd := m.press(key(tea.KeyEnter))
	run(cmd)
	started(t, turner.started)

	m = typed(m, "second")
	m, _ = m.press(key(tea.KeyEnter))
	m = typed(m, "still writing this one")

	m, drained := m.finish(turnDone{})

	if drained == nil {
		t.Fatal("the queue did not drain, so this test proves nothing about the draft")
	}
	if m.input.text != "still writing this one" {
		t.Errorf("the draft is now %q; the drain took the line being composed", m.input.text)
	}
	// And what went out is the queued message rather than the half-composed one.
	// Asserting the draft survived is not enough on its own: a drain reading the
	// input line leaves it exactly where it was and still sends the wrong text.
	run(drained)
	started(t, turner.started)
	if want := []string{"first", "second"}; !slices.Equal(turner.texts(), want) {
		t.Errorf("the model was sent %q, want %q", turner.texts(), want)
	}
}

// TestADrainedTurnRunsUnderItsOwnLiveContext pins where in finish the drain
// sits. Everything before it belongs to the turn that just ended — the
// interrupt that releases its context most of all — so a drain moved up would
// hand the queued message a context cancelled a line later, and the turn would
// die the moment it started.
func TestADrainedTurnRunsUnderItsOwnLiveContext(t *testing.T) {
	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, Config{}), "first")
	m, cmd := m.press(key(tea.KeyEnter))
	run(cmd)
	started(t, turner.started)

	m = typed(m, "second")
	m, _ = m.press(key(tea.KeyEnter))

	m, drained := m.finish(turnDone{})
	if drained == nil {
		t.Fatal("the queue did not drain, so there is no second turn to check")
	}
	if !m.busy || m.cancel == nil {
		t.Errorf("the drained turn left busy=%v and cancel=%v; nothing on screen says it is running "+
			"and no key can stop it", m.busy, m.cancel != nil)
	}

	run(drained)
	ctx := started(t, turner.started)
	select {
	case <-ctx.Done():
		t.Error("the drained turn started under a context that was already cancelled")
	case <-time.After(10 * time.Millisecond):
	}
}

// TestAQueuedDraftIsFlattenedForTheBlockButSentWhole. The block under the input
// is a reminder of what is waiting and gets one line per message; what reaches
// the model is what was typed, newlines and all.
func TestAQueuedDraftIsFlattenedForTheBlockButSentWhole(t *testing.T) {
	const pasted = "apply this hunk:\n--- a/auth.go\n+++ b/auth.go"

	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, Config{}), "first")
	m, cmd := m.press(key(tea.KeyEnter))
	run(cmd)
	started(t, turner.started)

	m = update(m, tea.PasteMsg{Content: pasted})
	m, _ = m.press(key(tea.KeyEnter))

	if !slices.Equal(m.queue, []string{pasted}) {
		t.Fatalf("the queue holds %q, want the pasted draft as it was typed", m.queue)
	}
	block := m.queued()
	if n := strings.Count(block, "\n"); n != 1 {
		t.Errorf("the queued block runs %d line(s) for one message and its header:\n%s", n+1, block)
	}
	if !strings.Contains(words(block), "apply this hunk: --- a/auth.go") {
		t.Errorf("the queued block does not show the message it is standing for:\n%s", block)
	}
}
