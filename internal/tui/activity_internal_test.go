package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tui/chat"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// TestTheActivityLineNamesWhatTheTurnIsActuallyDoing walks one turn through the
// states it passes and reads the verb back off the frame. The line is the only
// window the reader has onto which of them is true, so a verb chosen to look
// busy would be worse than no verb: the whole point of animating it is that a
// model thinking and a model hung stop looking the same.
func TestTheActivityLineNamesWhatTheTurnIsActuallyDoing(t *testing.T) {
	aloud := thought("The header is parsed before the body.", "")
	writing := thought("The header is parsed before the body.", "Reading `auth_test.go`")
	asked := asking(reply("Reading it."), llm.Block{Type: llm.BlockToolUse, ID: "call_1", Name: "read"})

	for _, tc := range []struct {
		name   string
		events []agent.Event
		want   string
	}{
		{name: "the prompt is sent and nothing has come back", want: "working"},
		{name: "thinking deltas are arriving", want: "thinking", events: []agent.Event{
			{Kind: agent.EventAssistantDelta, Message: aloud},
		}},
		{name: "the reply has started under the thinking", want: "writing", events: []agent.Event{
			{Kind: agent.EventAssistantDelta, Message: aloud},
			{Kind: agent.EventAssistantDelta, Message: writing},
		}},
		{name: "a call is running", want: "running read", events: []agent.Event{
			{Kind: agent.EventAssistantDelta, Message: writing},
			{Kind: agent.EventAssistantEnd, Message: asked},
			{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"},
		}},
		{name: "a batch is running", want: "running 2 tools", events: []agent.Event{
			{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"},
			{Kind: agent.EventToolStart, CallID: "call_2", Tool: "grep"},
		}},
		{name: "the batch is done and the next step has not started", want: "working", events: []agent.Event{
			{Kind: agent.EventAssistantEnd, Message: asked},
			{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"},
			{Kind: agent.EventToolEnd, CallID: "call_1", Tool: "read", Result: &tool.Result{Title: "read auth.go"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := busyModel(t)
			for _, ev := range tc.events {
				m = update(m, agentMsg{event: ev})
			}

			line := activityLine(t, m.View().Content)
			if !strings.Contains(line, tc.want) {
				t.Errorf("the activity line reads %q, and the turn is %q", line, tc.want)
			}
		})
	}
}

// TestTheLineNeverSaysThinkingWhileAToolRuns is the rule the case list above
// cannot state on its own. A step's thinking is still the last thing the model
// streamed when its calls are dispatched — nothing clears it — so the state
// that has to win is the one a check ordered the other way round would lose.
func TestTheLineNeverSaysThinkingWhileAToolRuns(t *testing.T) {
	m := busyModel(t)
	m = update(m, agentMsg{event: agent.Event{
		Kind: agent.EventAssistantDelta, Message: thought("Which file parses the header?", ""),
	}})

	if line := activityLine(t, m.View().Content); !strings.Contains(line, "thinking") {
		t.Fatalf("the line reads %q before any call ran, so the check below proves nothing", line)
	}

	m = update(m, agentMsg{event: agent.Event{Kind: agent.EventToolStart, CallID: "call_1", Tool: "grep"}})
	if line := activityLine(t, m.View().Content); strings.Contains(line, "thinking") {
		t.Errorf("the line still reads %q with a call in flight", line)
	}
}

// TestTheActivityLineAnimatesAndStopsWithTheTurn. Consecutive frames differing
// is the whole of what "animated" means from the outside, and the beat that
// produces them has to stop when the turn does: one that kept rescheduling
// would wake the program ten times a second for the rest of the session.
func TestTheActivityLineAnimatesAndStopsWithTheTurn(t *testing.T) {
	c := newClock(goldenNow)
	m := newModel(t.Context(), newTurner(nil), Config{})
	m.now = c.read
	m.width = goldenWidth
	m = update(m, agentMsg{event: agent.Event{
		Kind: agent.EventAssistantDelta, Message: reply("Reading it."),
	}})

	var (
		frames []string
		last   time.Duration
	)
	for range len(styles.Spinner) {
		frames = append(frames, activityLine(t, m.View().Content))

		elapsed := c.read().Sub(m.started)
		if elapsed < last {
			t.Fatalf("the turn's elapsed time went backwards, from %s to %s", last, elapsed)
		}
		last = elapsed

		c.pass(tickInterval)
		next, cmd := m.Update(tickMsg{})
		if cmd == nil {
			t.Fatal("the beat stopped scheduling itself mid-turn, so the line stopped moving")
		}
		m = next.(Model)
	}

	for i := 1; i < len(frames); i++ {
		if frames[i] == frames[i-1] {
			t.Fatalf("frame %d is the one before it, so the line is not animating: %q", i, frames[i])
		}
	}
	// Every glyph, not merely two that differ: a spinner reduced to one frame and
	// a blank would satisfy the loop above and read as a stutter on the screen.
	if seen := distinctFrames(frames); seen != len(styles.Spinner) {
		t.Errorf("%d distinct lines over %d beats, and the spinner has %d glyphs",
			seen, len(frames), len(styles.Spinner))
	}

	m = update(m, agentMsg{event: agent.Event{Kind: agent.EventTurnEnd}})
	if _, cmd := m.Update(tickMsg{}); cmd != nil {
		t.Error("the beat outlived the turn it was animating")
	}
	if line := strings.TrimSpace(activityLine(t, m.View().Content)); line != "" {
		t.Errorf("the line is still on the screen with no turn running: %q", line)
	}
}

// TestABeatRedrawsNoPartOfTheConversation is the freeze under the animation
// (internals §4.5): the activity line and the status line are drawn beside the
// conversation rather than as items in it, so ten beats a second cost no item
// render at all. Without a running call, because a card that is running is
// meant to be redrawn — this is about everything else.
func TestABeatRedrawsNoPartOfTheConversation(t *testing.T) {
	var runs int
	c := newClock(goldenNow)
	m := newModel(t.Context(), newTurner(nil), Config{Model: "anthropic/claude-opus-5"})
	m.now = c.read
	m.width = goldenWidth
	m.chat.Set("reply", counted{
		Message: chat.Message{Content: *reply("The header is parsed twice."), Done: true},
		runs:    &runs,
	})
	m = update(m, agentMsg{event: agent.Event{
		Kind: agent.EventAssistantDelta, Message: usage(llm.Usage{Input: 900, Output: 12}),
	}})

	frames := []string{m.View().Content}
	if runs == 0 {
		t.Fatal("the conversation was never drawn, so the count below is about a frame with nothing in it")
	}
	warm := runs

	for range 3 {
		c.pass(tickInterval)
		m = update(m, tickMsg{})
		frames = append(frames, m.View().Content)
	}

	for i := 1; i < len(frames); i++ {
		if frames[i] == frames[i-1] {
			t.Fatalf("frame %d is the one before it, so nothing moved and the count below is about a "+
				"UI that drew nothing:\n%s", i, frames[i])
		}
	}
	if redrawn := runs - warm; redrawn != 0 {
		t.Errorf("%d conversation item render(s) across %d beats that moved only the chrome",
			redrawn, len(frames)-1)
	}
}

// TestTheHintGoesBeforeTheStateDoes. The line is chrome and a narrow terminal
// is exactly where chrome must not push the conversation around, so it drops
// what it can spare: esc esc works whether or not there are columns to say so,
// and which state the turn is in is what nothing else on the screen says.
func TestTheHintGoesBeforeTheStateDoes(t *testing.T) {
	m := busyModel(t)
	m = update(m, agentMsg{event: agent.Event{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"}})

	for _, tc := range []struct {
		width int
		want  string
	}{
		{width: 0, want: "⠋ running read · esc esc to interrupt"},
		{width: 80, want: "⠋ running read · esc esc to interrupt"},
		{width: 30, want: "⠋ running read"},
		{width: 8, want: "⠋ runnin"},
	} {
		line := ansi.Strip(m.activity(tc.width))
		if line != tc.want {
			t.Errorf("at %d columns the line reads %q, want %q", tc.width, line, tc.want)
		}
		if tc.width > 0 && ansi.StringWidth(line) > tc.width {
			t.Errorf("the line is %d columns wide in a terminal %d wide", ansi.StringWidth(line), tc.width)
		}
	}
}

// TestAnArmedCancelReplacesTheHintAndKeepsTheState. Both arms take a second
// press (design §6 rule 7), and the line that carried the plain hint is the
// place to say so — while still naming the state, which is what the reader is
// deciding on.
func TestAnArmedCancelReplacesTheHintAndKeepsTheState(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
		want string
	}{
		{name: "esc", key: key(tea.KeyEscape), want: hintCancel},
		{name: "ctrl+c", key: ctrlCKey, want: hintQuit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := typed(newModel(t.Context(), newTurner(nil), Config{}), "read every file")
			m, _ = m.press(key(tea.KeyEnter))
			m, _ = m.press(tc.key)

			line := activityLine(t, m.View().Content)
			switch {
			case !strings.Contains(line, tc.want):
				t.Errorf("the line reads %q and the arm wants %q", line, tc.want)
			case !strings.Contains(line, "working"):
				t.Errorf("the line stopped naming the turn's state while armed: %q", line)
			case strings.Contains(line, hintInterrupt):
				t.Errorf("the line carries both the arm and the plain hint: %q", line)
			}
		})
	}
}

// TestTheElapsedTimeAppearsOnlyOnceThereIsOneToShow. A turn measured in
// milliseconds is over before a reader could look at the number, and a line
// that opened with "0s" on it would make every turn look stalled at the start.
func TestTheElapsedTimeAppearsOnlyOnceThereIsOneToShow(t *testing.T) {
	for _, tc := range []struct {
		elapsed time.Duration
		want    string
	}{
		{elapsed: 0, want: ""},
		{elapsed: 40 * time.Millisecond, want: ""},
		{elapsed: 1500 * time.Millisecond, want: "1.5s"},
		{elapsed: 65 * time.Second, want: "1m5s"},
	} {
		if got := duration(tc.elapsed); got != tc.want {
			t.Errorf("duration(%s) = %q, want %q", tc.elapsed, got, tc.want)
		}
	}
}

// busyModel is a turn that has started and produced nothing yet, with the clock
// stopped where it began — so nothing below reads an elapsed time that depends
// on the machine. The idle session it is built on stops the two drifting apart
// (frame_internal_test.go).
func busyModel(t *testing.T) Model {
	t.Helper()
	return idleModel(t).busied()
}

// activityLine picks the line out of a frame, matched on the spinner rather
// than on a position: the conversation above it is any number of lines tall.
// Empty means the frame has none, which is a state the callers assert on.
func activityLine(t *testing.T, frame string) string {
	t.Helper()

	var found []string
	for _, row := range strings.Split(ansi.Strip(frame), "\n") {
		for _, glyph := range styles.Spinner {
			if strings.HasPrefix(row, glyph) {
				found = append(found, row)
				break
			}
		}
	}
	if len(found) > 1 {
		t.Fatalf("%d lines in the frame open with a spinner glyph, and the activity line is one:\n%s",
			len(found), frame)
	}
	if len(found) == 0 {
		return ""
	}
	return found[0]
}

func distinctFrames(frames []string) int {
	seen := make(map[string]bool, len(frames))
	for _, f := range frames {
		seen[f] = true
	}
	return len(seen)
}
