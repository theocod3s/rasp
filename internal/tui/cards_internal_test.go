package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tool"
)

// TestCardsAreDrawnInTheOrderTheModelAskedFor. A batch runs its calls at once,
// so tool_start arrives in whatever order the scheduler got to them — and a
// card list built from those alone puts the transcript in an order the model
// never asked for. The starts below arrive backwards for exactly that reason.
func TestCardsAreDrawnInTheOrderTheModelAskedFor(t *testing.T) {
	asked := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockText, Text: "Reading, then editing."},
		{Type: llm.BlockToolUse, ID: "call_1", Name: "read"},
		{Type: llm.BlockToolUse, ID: "call_2", Name: "edit"},
		{Type: llm.BlockToolUse, ID: "call_3", Name: "grep"},
	}}

	var m tea.Model = Model{}
	for _, ev := range []agent.Event{
		{Kind: agent.EventAssistantDelta, Message: asked},
		{Kind: agent.EventAssistantEnd, Message: asked},
		{Kind: agent.EventToolStart, CallID: "call_3", Tool: "grep"},
		{Kind: agent.EventToolStart, CallID: "call_2", Tool: "edit"},
		{Kind: agent.EventToolEnd, CallID: "call_2", Tool: "edit", Result: &tool.Result{Title: "auth.go +3 -1"}},
		{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"},
	} {
		m, _ = m.Update(agentMsg{event: ev})
	}

	frame := words(m.View().Content)
	read, edit, grep := strings.Index(frame, "read running"),
		strings.Index(frame, "edit auth.go"),
		strings.Index(frame, "grep running")
	switch {
	case read < 0 || edit < 0 || grep < 0:
		t.Fatalf("one of the three calls never reached the conversation:\n%s", frame)
	case !(read < edit && edit < grep):
		t.Errorf("the cards read in the order the calls were scheduled rather than the order the model "+
			"asked for them:\n%s", frame)
	}

	// Four items: the reply, and one card per call — the announcement creates
	// each card once, and the start and end of a call update it where it stands.
	if root, held := m.(Model), 4; root.chat.Len() != held {
		t.Errorf("the conversation holds %d items, and one reply with three calls is %d", root.chat.Len(), held)
	}
}

// TestACallAnnouncedAndNeverStartedIsStillDrawn. Nothing runs a call the batch
// was cancelled before reaching, and a card that quietly vanished would leave
// the reader believing the model never asked for it.
func TestACallAnnouncedAndNeverStartedIsStillDrawn(t *testing.T) {
	asked := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockToolUse, ID: "call_1", Name: "read"},
		{Type: llm.BlockToolUse, ID: "call_2", Name: "bash"},
	}}

	var m tea.Model = Model{}
	for _, ev := range []agent.Event{
		{Kind: agent.EventAssistantEnd, Message: asked},
		{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"},
		{Kind: agent.EventTurnEnd},
	} {
		m, _ = m.Update(agentMsg{event: ev})
	}

	if frame, want := words(m.View().Content), "bash queued"; !strings.Contains(frame, want) {
		t.Errorf("the frame does not mention %q, so the call the turn ended before is gone:\n%s", want, frame)
	}
}

// TestATickMovesTheRunningCardAndLeavesTheFinishedOnesAlone is internals §4.5's
// freeze under a clock: the beat exists to redraw a call still running, and a
// beat that wrote back every card would drop the whole conversation's cache
// ten times a second. A finished card carries the time its call took, so one
// redrawn here says a longer time on the next frame and this test says so.
func TestATickMovesTheRunningCardAndLeavesTheFinishedOnesAlone(t *testing.T) {
	const finished = "read auth.go (42 lines) 2s"

	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	var m tea.Model = Model{now: func() time.Time { return now }}

	// A delta first, so the turn reads as running: a beat with no turn behind it
	// declines to schedule another whatever the cards say, and the last check
	// below would then pass for a reason it is not testing.
	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventAssistantDelta, Message: reply("Reading it.")}})
	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"}})
	now = now.Add(2 * time.Second)
	m, _ = m.Update(agentMsg{event: agent.Event{
		Kind: agent.EventToolEnd, CallID: "call_1", Tool: "read",
		Result: &tool.Result{Title: "read auth.go (42 lines)"},
	}})
	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventToolStart, CallID: "call_2", Tool: "bash"}})
	started := now

	if frame := words(m.View().Content); !strings.Contains(frame, finished) {
		t.Fatalf("the frame does not read %q, so nothing below can tell it apart from one that moved:\n%s",
			finished, frame)
	}

	for _, tc := range []struct {
		after   time.Duration
		running string
	}{
		{after: 1500 * time.Millisecond, running: "bash running 1.5s"},
		{after: 7 * time.Second, running: "bash running 7s"},
	} {
		now = started.Add(tc.after)
		m, _ = m.Update(tickMsg{})

		frame := words(m.View().Content)
		if !strings.Contains(frame, tc.running) {
			t.Errorf("the frame does not read %q, so the beat did not move the running call:\n%s", tc.running, frame)
		}
		if !strings.Contains(frame, finished) {
			t.Errorf("the frame no longer reads %q, so the beat redrew a card that had already "+
				"finished — and dropped its frozen render with it:\n%s", finished, frame)
		}
	}

	// And the beat stops once nothing is running, rather than waking the program
	// for the rest of the session.
	m, _ = m.Update(agentMsg{event: agent.Event{
		Kind: agent.EventToolEnd, CallID: "call_2", Tool: "bash", Result: &tool.Result{Title: "go test"},
	}})
	if _, cmd := m.(Model).beat(); cmd != nil {
		t.Error("the beat scheduled another with no call left running")
	}
}

// TestTheBeatStopsWithTheTurnThatStartedIt. Every call the loop starts gets a
// tool_end, so a card still running once the turn is over is one whose end went
// missing — and a beat that kept rescheduling on it would wake the program ten
// times a second for the rest of the session.
func TestTheBeatStopsWithTheTurnThatStartedIt(t *testing.T) {
	var m tea.Model = Model{}
	for _, ev := range []agent.Event{
		{Kind: agent.EventAssistantDelta, Message: reply("Running the tests.")},
		{Kind: agent.EventToolStart, CallID: "call_1", Tool: "bash"},
	} {
		m, _ = m.Update(agentMsg{event: ev})
	}

	if _, cmd := m.(Model).beat(); cmd == nil {
		t.Fatal("the beat stopped while a call was still running mid-turn")
	}

	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventTurnEnd}})
	if _, cmd := m.(Model).beat(); cmd != nil {
		t.Error("the beat outlived the turn, with a card left running and nothing to finish it")
	}
}

// TestATickIsScheduledOnceHoweverManyCallsStart. Four calls start at once in a
// batch, and a timer for each would leave four of them running for the rest of
// the session, every one rescheduling itself.
func TestATickIsScheduledOnceHoweverManyCallsStart(t *testing.T) {
	var m tea.Model = Model{}

	var scheduled int
	for _, id := range []string{"call_1", "call_2", "call_3", "call_4"} {
		var cmd tea.Cmd
		m, cmd = m.Update(agentMsg{event: agent.Event{Kind: agent.EventToolStart, CallID: id, Tool: "read"}})
		if cmd != nil {
			scheduled++
		}
	}

	if scheduled != 1 {
		t.Errorf("four calls starting scheduled %d beats, want 1", scheduled)
	}
}

// TestExpandingShowsWhatEveryCallProduced. The whole conversation rather than
// one card, because nothing selects one: there is no cursor over the transcript
// for "this card" to mean.
func TestExpandingShowsWhatEveryCallProduced(t *testing.T) {
	const output = "PASS ok github.com/theocod3s/rasp/internal/auth"

	var m tea.Model = Model{}
	for _, ev := range []agent.Event{
		{Kind: agent.EventToolStart, CallID: "call_1", Tool: "bash"},
		{Kind: agent.EventToolEnd, CallID: "call_1", Tool: "bash", Result: &tool.Result{
			Title:   "go test ./...",
			Content: output,
		}},
	} {
		m, _ = m.Update(agentMsg{event: ev})
	}

	if frame := words(m.View().Content); strings.Contains(frame, output) {
		t.Fatalf("the card is open before anyone asked:\n%s", frame)
	}

	m, _ = m.Update(expandKey)
	if frame := words(m.View().Content); !strings.Contains(frame, output) {
		t.Errorf("the card did not open:\n%s", frame)
	}

	m, _ = m.Update(expandKey)
	if frame := words(m.View().Content); strings.Contains(frame, output) {
		t.Errorf("the card did not close again:\n%s", frame)
	}
}

// TestAFileChangeIsDrawnWithoutAnyoneAskingForIt. Every other card opens on a
// keypress, and a diff behind one is a transcript whose default state is a path
// and a line count — the gap in the reference implementations that this UI
// exists to close. The toggle still wins afterwards, so a reader who wants the
// conversation small can have it.
func TestAFileChangeIsDrawnWithoutAnyoneAskingForIt(t *testing.T) {
	const line = "-return parse(header)"

	var m tea.Model = Model{}
	for _, ev := range []agent.Event{
		{Kind: agent.EventToolStart, CallID: "call_1", Tool: "edit"},
		{Kind: agent.EventToolEnd, CallID: "call_1", Tool: "edit", Result: edit()},
	} {
		m, _ = m.Update(agentMsg{event: ev})
	}

	if frame := words(m.View().Content); !strings.Contains(frame, line) {
		t.Fatalf("the diff is behind a keypress:\n%s", frame)
	}

	// One press, not two. A toggle that remembered the last press rather than
	// reading the cards would do nothing here — the flag would say closed while
	// the screen said open, and the press would only bring the two into
	// agreement — so the reader presses the close key and the frame stands.
	m, _ = m.Update(expandKey)
	if frame := words(m.View().Content); strings.Contains(frame, line) {
		t.Errorf("the first press did not close a card that opened itself:\n%s", frame)
	}

	m, _ = m.Update(expandKey)
	if frame := words(m.View().Content); !strings.Contains(frame, line) {
		t.Errorf("the next press did not open it again:\n%s", frame)
	}
}

// TestACollapsedConversationStaysCollapsedAsMoreChangesLand. A diff opens
// itself, and a reader who does not want that says so once. A turn making
// eight edits must not ask them eight more times — which is what a card
// deciding for itself on every result, with nothing recording that the reader
// had already answered, would do.
func TestACollapsedConversationStaysCollapsedAsMoreChangesLand(t *testing.T) {
	const line = "-return parse(header)"

	var m tea.Model = Model{}
	m, _ = m.Update(agentMsg{event: agent.Event{
		Kind: agent.EventToolEnd, CallID: "call_1", Tool: "edit", Result: edit(),
	}})
	m, _ = m.Update(expandKey)

	if frame := words(m.View().Content); strings.Contains(frame, line) {
		t.Fatalf("the press did not collapse the conversation:\n%s", frame)
	}

	m, _ = m.Update(agentMsg{event: agent.Event{
		Kind: agent.EventToolEnd, CallID: "call_2", Tool: "edit", Result: edit(),
	}})
	if frame := words(m.View().Content); strings.Contains(frame, line) {
		t.Errorf("the next change opened itself over the reader's answer:\n%s", frame)
	}

	// And the reader can still get all of it back.
	m, _ = m.Update(expandKey)
	if frame := words(m.View().Content); !strings.Contains(frame, line) {
		t.Errorf("nothing reopened:\n%s", frame)
	}
}

// TestTheToggleStillTogglesWithNothingToRead. Reading the direction off the
// cards has nothing to read before any tool has run, and "nothing is open"
// every time means opening every time — so two presses would leave the
// conversation set to open, and the next call would arrive with its body
// showing.
func TestTheToggleStillTogglesWithNothingToRead(t *testing.T) {
	var m tea.Model = Model{}
	m, _ = m.Update(expandKey)
	m, _ = m.Update(expandKey)

	m, _ = m.Update(agentMsg{event: agent.Event{
		Kind: agent.EventToolEnd, CallID: "call_1", Tool: "read",
		Result: &tool.Result{Title: "read auth.go (1 line)", Content: "1\tpackage auth"},
	}})

	if frame := words(m.View().Content); strings.Contains(frame, "package auth") {
		t.Errorf("the card arrived open after two presses that should have cancelled out:\n%s", frame)
	}
}

// TestALightTerminalRedrawsEveryDiffInItsOwnPalette. The background query is
// answered after the program has drawn its first frames, and a finished card's
// render is frozen at the colours it was drawn in — so an answer that only
// reached the cards after it would leave the conversation half in each palette.
func TestALightTerminalRedrawsEveryDiffInItsOwnPalette(t *testing.T) {
	edited := func() tea.Model {
		var m tea.Model = Model{}
		for _, ev := range []agent.Event{
			{Kind: agent.EventToolStart, CallID: "call_1", Tool: "edit"},
			{Kind: agent.EventToolEnd, CallID: "call_1", Tool: "edit", Result: edit()},
		} {
			m, _ = m.Update(agentMsg{event: ev})
		}
		return m
	}

	unasked := edited().View().Content

	answered, _ := edited().Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#ffffff")})
	onLight := answered.View().Content

	switch {
	case unasked == onLight:
		t.Fatal("a light terminal drew the same bytes as one that never answered, so the answer " +
			"reached nothing already on the screen")
	case words(unasked) != words(onLight):
		t.Errorf("the answer changed the text and not only the colours:\n%s\n\n%s", words(unasked), words(onLight))
	}

	// A dark answer leaves the frame as it already was, which is what makes the
	// unanswered default a whole palette rather than a missing one.
	same, _ := edited().Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#000000")})
	if got := same.View().Content; got != unasked {
		t.Errorf("a dark terminal drew something other than the default:\n%s\n\n%s", unasked, got)
	}

	// And a call that arrives after the answer is drawn in the palette too. The
	// redraw above reaches the cards that already exist; a card built later is
	// handed the background instead, and only one of the two routes being wired
	// leaves a conversation half in each palette.
	later, _ := answered.Update(agentMsg{event: agent.Event{
		Kind: agent.EventToolEnd, CallID: "call_2", Tool: "edit", Result: edit(),
	}})
	deleted := opening(t, later.View().Content, "-return parse(header)")
	if len(deleted) != 2 {
		t.Fatalf("%d rows delete that line, and two cards each drawing the diff is 2:\n%s",
			len(deleted), later.View().Content)
	}
	if deleted[0] != deleted[1] {
		t.Errorf("the card drawn before the answer opens %q and the one drawn after opens %q",
			deleted[0], deleted[1])
	}
}

// opening is the escape sequence each row saying want begins with. A row with
// none contributes an empty string rather than being skipped, so a class that
// lost its colour reads as a difference instead of as nothing to compare.
func opening(t *testing.T, frame, want string) []string {
	t.Helper()

	var found []string
	for _, row := range strings.Split(frame, "\n") {
		if !strings.Contains(words(row), want) {
			continue
		}
		var code string
		if i := strings.IndexByte(row, 0x1b); i >= 0 {
			if j := strings.IndexByte(row[i:], 'm'); j >= 0 {
				code = row[i : i+j+1]
			}
		}
		found = append(found, code)
	}
	return found
}

// edit is a finished edit carrying the diff it applied.
func edit() *tool.Result {
	return &tool.Result{
		Title: "auth.go +1 -1",
		Details: &tool.DiffDetails{Path: "auth.go", Additions: 1, Deletions: 1, Unified: "--- a/auth.go\n" +
			"+++ b/auth.go\n@@ -8,2 +8,2 @@\n-return parse(header)\n+return r.claims()\n"},
	}
}

// expandKey is the binding that opens every card. Ctrl rather than a bare
// letter because the input line takes every printable key.
var expandKey = tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
