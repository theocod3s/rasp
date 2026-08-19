package tui

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/tool"
)

// The terminal every frame is recorded at. A reply is wrapped and padded to the
// width it was drawn for, so a golden taken at one size and compared at another
// differs on every line and says nothing about the change that caused it.
const goldenWidth, goldenHeight = 80, 24

// snapshot is one state of the UI worth freezing: the prompt that starts a turn,
// and the events the loop then emits into it.
type snapshot struct {
	name   string
	prompt string
	events []agent.Event
}

// snapshots are the states a golden is kept for. Deliberately few: every ticket
// that changes how the UI draws pays to regenerate all of them, so this holds
// the states that answer a different question about the frame, not every state
// reachable.
func snapshots() []snapshot {
	const prompt = "fix the failing auth test"

	fragment := reply("Reading `auth_test.go` now. The header is parsed")
	explained := reply("Reading `auth_test.go` now. The header is parsed **twice**.\n\n" +
		"- once in the middleware\n- once in the handler\n")
	fixed := reply("Both call sites read the parsed header instead of parsing it again.")

	return []snapshot{
		{name: "empty"},
		{name: "busy", prompt: prompt},
		{name: "streaming", prompt: prompt, events: []agent.Event{
			{Kind: agent.EventAssistantDelta, Message: fragment},
		}},
		// Two calls in flight at once, finishing out of order, one of them failed:
		// the transcript has to keep each where it started (design §6 rule 6).
		{name: "tools", prompt: prompt, events: []agent.Event{
			{Kind: agent.EventAssistantDelta, Message: explained},
			{Kind: agent.EventAssistantEnd, Message: explained},
			{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"},
			{Kind: agent.EventToolStart, CallID: "call_2", Tool: "edit"},
			{Kind: agent.EventToolEnd, CallID: "call_2", Tool: "edit", Result: &tool.Result{IsError: true}},
			{Kind: agent.EventToolEnd, CallID: "call_1", Tool: "read", Result: &tool.Result{}},
			{Kind: agent.EventAssistantDelta, Message: fixed},
			{Kind: agent.EventAssistantEnd, Message: fixed},
			{Kind: agent.EventTurnEnd},
		}},
		// A step that failed mid-reply: the fragment is settled rather than left
		// open, and the failure is drawn under it.
		{name: "error", prompt: prompt, events: []agent.Event{
			{Kind: agent.EventAssistantDelta, Message: fragment},
			{Kind: agent.EventError, Err: errors.New("the provider closed the stream mid-message")},
			{Kind: agent.EventTurnEnd},
		}},
	}
}

// TestViewGoldens freezes the frame each state draws, so a change to any of the
// styling, the wrapping or the order things appear in shows up as a diff rather
// than as nothing at all.
//
// Regenerate with `go test ./internal/tui -update` — the flag belongs to the
// golden package, which is why nothing here declares it. Regenerate the whole
// test rather than one subtest: the markdown renderer memoises the head of a
// reply across frames (internals §4.4), so a run that skips its neighbours does
// not necessarily reach a golden by the same path the suite does.
func TestViewGoldens(t *testing.T) {
	// The one thing about the frame that is not the UI's to decide. Glamour
	// measures a reply in terminal cells, and charmbracelet/x/ansi widens every
	// East Asian ambiguous character — the bullet it draws a list with among them
	// — to two of them when RUNEWIDTH_EASTASIAN is set; it reads the variable once
	// at init and offers no override. Named here because the diff on its own
	// reads as a styling regression nobody made.
	if wide, err := strconv.ParseBool(os.Getenv("RUNEWIDTH_EASTASIAN")); err == nil && wide {
		t.Fatal("RUNEWIDTH_EASTASIAN is set and these frames were recorded without it; unset it to " +
			"compare them, or every diff below is the width of a bullet rather than a change to the UI")
	}

	states := snapshots()
	if len(states) == 0 {
		t.Fatal("there are no states to record, and a golden suite that snapshots nothing passes forever")
	}

	drawn := make(map[string]string, len(states))
	for _, state := range states {
		t.Run(state.name, func(t *testing.T) {
			frame := draw(t, state)
			if strings.TrimSpace(frame) == "" {
				t.Fatal("the state drew a blank frame, which every other state would match too")
			}
			drawn[state.name] = frame
			golden.RequireEqual(t, frame)
		})
	}

	distinct(t, drawn)
	recorded(t, states)
}

// TestAResizeRedrawsTheConversationAtItsNewWidth is what the goldens cannot say
// on their own: a snapshot records whatever the harness produced, so something
// asserted rather than recorded has to prove the program is really running the
// model — here a terminal that changed size after the conversation had lines in
// it, which is the resize a golden taken at a fixed width never sees.
func TestAResizeRedrawsTheConversationAtItsNewWidth(t *testing.T) {
	const (
		prompt = "fix the failing auth test and say which middleware parsed the header twice"
		narrow = 24
	)

	tm, turner := program(t)
	submit(t, tm, turner, prompt)
	tm.Send(tea.WindowSizeMsg{Width: narrow, Height: goldenHeight})

	frame := quit(t, tm).View().Content
	if !strings.Contains(words(frame), prompt) {
		t.Fatalf("the prompt never reached the conversation:\n%s", frame)
	}
	for _, line := range strings.Split(frame, "\n") {
		if n := utf8.RuneCountInString(line); n > narrow {
			t.Errorf("a line runs %d columns into a terminal %d wide, so the resize was never drawn: %q",
				n, narrow, line)
		}
	}
}

// draw runs one state through a real Bubble Tea program and returns the frame it
// ended on.
func draw(t *testing.T, state snapshot) string {
	t.Helper()

	tm, turner := program(t)
	if state.prompt != "" {
		submit(t, tm, turner, state.prompt)
	}
	for _, ev := range state.events {
		tm.Send(agentMsg{event: ev})
	}

	return quit(t, tm).View().Content
}

// program starts the root model under teatest, with no terminal at either end.
// The renderer is off because nothing here reads the output stream, and View is
// called after every Update all the same.
//
// The turner it comes back with stays in Send until the test's context ends,
// which is what holds a turn open long enough for a frame to be taken mid-flight.
func program(t *testing.T) (*teatest.TestModel, *turner) {
	t.Helper()

	turner := newTurner(agent.ErrInterrupted)
	return teatest.NewTestModel(t, newModel(t.Context(), turner),
		teatest.WithInitialTermSize(goldenWidth, goldenHeight),
		teatest.WithProgramOptions(tea.WithoutRenderer()),
	), turner
}

// submit types a line, sends it, and waits for the turn it starts. The turn runs
// on a goroutine of its own, so waiting is what orders everything after it
// rather than against it: the turn's own turnDone landing in the middle leaves
// the frame busy or idle depending on the scheduler.
func submit(t *testing.T, tm *teatest.TestModel, turn *turner, text string) {
	t.Helper()

	tm.Type(text)
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	started(t, turn.started)
}

// quit stops the program and hands back the model it ended on. Quit travels the
// same channel as everything sent before it, so the program stops having handled
// all of it rather than at whatever point it had reached.
func quit(t *testing.T, tm *teatest.TestModel) Model {
	t.Helper()

	if err := tm.Quit(); err != nil {
		t.Fatalf("quitting the program: %v", err)
	}
	last := tm.FinalModel(t, teatest.WithFinalTimeout(settle))
	final, ok := last.(Model)
	if !ok {
		t.Fatalf("the program returned a %T rather than the root model", last)
	}
	return final
}

// distinct fails when two states drew the same frame, and is what stands
// between this suite and the quietest failure a golden test has. A harness that
// stopped delivering anything to the model still records five frames, all of
// them the empty one — and -update then makes that the expectation, for good.
func distinct(t *testing.T, drawn map[string]string) {
	t.Helper()

	by := make(map[string]string, len(drawn))
	for _, name := range slices.Sorted(maps.Keys(drawn)) {
		if first, ok := by[drawn[name]]; ok {
			t.Errorf("%q drew the same frame as %q, so one of the two is not the state it is named for", name, first)
			continue
		}
		by[drawn[name]] = name
	}
}

// recorded checks what is on disk against the states above. Either half alone is
// quiet about the failure that matters: a golden no state names is compared to
// nothing for the rest of its life, and a state with no golden is a frame nobody
// has ever looked at — which -update would then write and call a pass.
func recorded(t *testing.T, states []snapshot) {
	t.Helper()

	files, err := filepath.Glob(filepath.Join("testdata", t.Name(), "*.golden"))
	if err != nil {
		t.Fatalf("looking for the recorded frames: %v", err)
	}

	have := make([]string, 0, len(files))
	for _, f := range files {
		have = append(have, strings.TrimSuffix(filepath.Base(f), ".golden"))
	}
	want := make([]string, 0, len(states))
	for _, state := range states {
		want = append(want, state.name)
	}
	slices.Sort(have)
	slices.Sort(want)

	if !slices.Equal(have, want) {
		t.Errorf("testdata holds %v and the states are %v; regenerate every golden with -update, "+
			"and delete by hand the ones no state names any more", have, want)
	}
}
