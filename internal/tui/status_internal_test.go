package tui

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tui/chat"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// TestTheModeIsOnEveryFrame. Which mode a session is in is the question the
// permissive ones make expensive to get wrong (design §7.8), so it is on the
// screen in states where there is nothing else to draw at all and in states
// where something has gone wrong — the two a line built only for the ordinary
// case tends to miss.
func TestTheModeIsOnEveryFrame(t *testing.T) {
	asked := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockText, Text: "Reading it."},
		{Type: llm.BlockToolUse, ID: "call_1", Name: "read"},
	}}

	for _, tc := range []struct {
		name   string
		width  int
		events []agent.Event
	}{
		{name: "nothing has happened yet", width: goldenWidth},
		{name: "before the terminal has reported a size"},
		{name: "mid-stream", width: goldenWidth, events: []agent.Event{
			{Kind: agent.EventAssistantDelta, Message: asked},
		}},
		{name: "a tool is running", width: goldenWidth, events: []agent.Event{
			{Kind: agent.EventAssistantEnd, Message: asked},
			{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"},
		}},
		{name: "the turn failed", width: goldenWidth, events: []agent.Event{
			{Kind: agent.EventError, Err: errors.New("the provider closed the stream mid-message")},
			{Kind: agent.EventTurnEnd},
		}},
		{name: "the terminal is too narrow for anything else", width: 12, events: []agent.Event{
			{Kind: agent.EventTurnEnd, Usage: llm.Usage{Input: 900, Output: 120}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m tea.Model = newModel(t.Context(), newTurner(nil), Config{
				Model: "anthropic/claude-opus-5",
				Mode:  permission.ModePlan,
			})
			m, _ = m.Update(tea.WindowSizeMsg{Width: tc.width, Height: goldenHeight})
			for _, ev := range tc.events {
				m, _ = m.Update(agentMsg{event: ev})
			}

			if frame := words(m.View().Content); !strings.Contains(frame, string(permission.ModePlan)) {
				t.Errorf("the frame never names the mode:\n%s", frame)
			}
		})
	}
}

// TestAModelNobodyNamedStillDrawsAMode. Every construction path has to satisfy
// the rule above, and the zero Model is the one that would not: it is what the
// tests around it build, and it is what a caller who passed no Config gets.
func TestAModelNobodyNamedStillDrawsAMode(t *testing.T) {
	frame := words(Model{}.View().Content)
	if !strings.Contains(frame, string(permission.ModeManual)) {
		t.Errorf("a model configured with nothing draws no mode, and manual is the default it "+
			"would be running under:\n%s", frame)
	}
}

// TestTheLineCountsWhatTheTurnSpent walks a two-step turn and reads the numbers
// off the frame. The counts are the provider's, summed here the way the loop
// sums them for itself.
func TestTheLineCountsWhatTheTurnSpent(t *testing.T) {
	first := reply("Reading it.")
	first.Usage = llm.Usage{Input: 400, Output: 60, CacheRead: 11000, CacheWrite: 200}
	second := reply("The header is parsed twice.")
	second.Usage = llm.Usage{Input: 700, Output: 90, CacheRead: 11200}

	var m tea.Model = newModel(t.Context(), newTurner(nil), Config{Mode: permission.ModeManual})
	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventAssistantEnd, Message: first}})

	// The first call's own counts, prompt and reply together — cache hits and
	// writes are prompt, and Input excludes both.
	if frame, want := words(m.View().Content), "ctx 11.7k"; !strings.Contains(frame, want) {
		t.Errorf("the frame does not read %q:\n%s", want, frame)
	}

	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventAssistantEnd, Message: second}})
	frame := words(m.View().Content)
	for _, want := range []string{"ctx 12k", "in 23.5k", "out 150"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the frame does not read %q:\n%s", want, frame)
		}
	}
}

// TestATurnsTotalReplacesTheRunningSumRatherThanAddingToIt. The loop sums a
// turn's usage over the same messages the UI has already counted one at a time
// (agent/step.go), so a session total that added the turn's own report to its
// running sum would say double after the first turn and stay wrong for the rest
// of the session.
func TestATurnsTotalReplacesTheRunningSumRatherThanAddingToIt(t *testing.T) {
	step := reply("Done.")
	step.Usage = llm.Usage{Input: 1200, Output: 300, CacheRead: 4000}
	total := llm.Usage{Input: 1200, Output: 300, CacheRead: 4000}

	var m tea.Model = newModel(t.Context(), newTurner(nil), Config{Mode: permission.ModeManual})
	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventAssistantEnd, Message: step}})

	before := words(m.View().Content)
	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventTurnEnd, Usage: total}})

	if after := words(m.View().Content); after != before {
		t.Errorf("the turn ending changed the counts:\nbefore %s\nafter  %s", before, after)
	}
	for _, want := range []string{"in 5.2k", "out 300"} {
		if !strings.Contains(before, want) {
			t.Fatalf("the frame does not read %q, so the comparison above holds over the wrong "+
				"numbers:\n%s", want, before)
		}
	}
}

// TestAStatusUpdateRedrawsNoPartOfTheConversation is the freeze from the other
// side (internals §4.5): the status line is rendered beside the conversation
// rather than as an item in it, so a frame in which only these numbers moved
// costs no item render at all. The count is the assertion, and the frames
// having changed is what stops it from passing on a line that says nothing.
func TestAStatusUpdateRedrawsNoPartOfTheConversation(t *testing.T) {
	var runs int
	m := newModel(t.Context(), newTurner(nil), Config{
		Model: "anthropic/claude-opus-5",
		Mode:  permission.ModeManual,
	})
	m.width = goldenWidth
	m.chat.Set("reply", counted{
		Message: chat.Message{Content: *reply("The header is parsed twice."), Done: true},
		runs:    &runs,
	})

	frames := []string{m.View().Content}
	if runs != 1 {
		t.Fatalf("the first frame drew the conversation %d time(s); the count below rests on the "+
			"item having been drawn exactly once already", runs)
	}
	warm := runs

	var tm tea.Model = m
	for _, u := range []llm.Usage{
		{Input: 900, Output: 120, CacheRead: 8000},
		{Input: 1400, Output: 260, CacheRead: 8000},
	} {
		tm, _ = tm.Update(agentMsg{event: agent.Event{Kind: agent.EventAssistantEnd, Message: usage(u)}})
		tm, _ = tm.Update(agentMsg{event: agent.Event{Kind: agent.EventTurnEnd, Usage: u}})
		frames = append(frames, tm.View().Content)
	}

	for i := 1; i < len(frames); i++ {
		if frames[i] == frames[i-1] {
			t.Fatalf("frame %d is the one before it, so nothing about the status line moved and the "+
				"count below is about a UI that drew nothing:\n%s", i, frames[i])
		}
	}
	if redrawn := runs - warm; redrawn != 0 {
		t.Errorf("%d conversation item render(s) across %d frames in which only the status line "+
			"changed", redrawn, len(frames)-1)
	}
}

// TestSegmentsDropFromTheRightAndTheModeStays. A narrow terminal is where the
// status line matters most and has least room, so what goes is decided rather
// than left to the wrap: whole segments from the right, the mode last of all.
func TestSegmentsDropFromTheRightAndTheModeStays(t *testing.T) {
	s := status{
		model:   "anthropic/claude-opus-5",
		mode:    permission.ModeManual,
		context: 12_800,
		spent:   llm.Usage{Input: 2342, Output: 205, CacheRead: 22408},
	}

	for _, tc := range []struct {
		width int
		want  string
	}{
		{width: 0, want: "manual · anthropic/claude-opus-5 · ctx 12.8k · in 24.8k · out 205 · cost —"},
		{width: 80, want: "manual · anthropic/claude-opus-5 · ctx 12.8k · in 24.8k · out 205 · cost —"},
		{width: 70, want: "manual · anthropic/claude-opus-5 · ctx 12.8k · in 24.8k · out 205"},
		{width: 60, want: "manual · anthropic/claude-opus-5 · ctx 12.8k · in 24.8k"},
		{width: 40, want: "manual · anthropic/claude-opus-5"},
		{width: 20, want: "manual"},
		{width: 4, want: "manu"},
	} {
		t.Run(strconv.Itoa(tc.width)+" columns", func(t *testing.T) {
			line := ansi.Strip(s.Render(tc.width, styles.Dark))
			if line != tc.want {
				t.Errorf("at %d columns the line reads\n%q\nwant\n%q", tc.width, line, tc.want)
			}
			if tc.width > 0 && ansi.StringWidth(line) > tc.width {
				t.Errorf("the line is %d columns wide in a terminal %d wide", ansi.StringWidth(line), tc.width)
			}
		})
	}
}

// TestTheModesWithSomethingToSayAreColoured. Plan and auto change what a tool
// may do without being asked, and each has a colour of its own; manual is the
// default and draws in the terminal's foreground (design §7.8).
func TestTheModesWithSomethingToSayAreColoured(t *testing.T) {
	drawn := func(mode permission.Mode) string {
		return status{mode: mode}.Render(0, styles.Dark)
	}

	if line := drawn(permission.ModeManual); !strings.HasPrefix(line, string(permission.ModeManual)) {
		t.Errorf("manual opens with an escape sequence, so it is not the terminal's own colour: %q", line)
	}

	// The escape a mode's own name is drawn behind, which is what a colour is on
	// a line whose remaining segments are coloured too.
	tint := func(mode permission.Mode) string {
		line := drawn(mode)
		return line[:strings.Index(line, string(mode))]
	}
	for _, mode := range []permission.Mode{permission.ModePlan, permission.ModeAuto} {
		if tint(mode) == "" {
			t.Errorf("%s is drawn in no colour at all: %q", mode, drawn(mode))
		}
	}
	if tint(permission.ModePlan) == tint(permission.ModeAuto) {
		t.Error("plan and auto are drawn in the same colour, so the line says which mode only in words")
	}

	// A mode this build has no token for draws plainly rather than borrowing one
	// of the two above, which would say something about it that nothing knows.
	if line := drawn("yolo"); !strings.HasPrefix(line, "yolo") {
		t.Errorf("an unknown mode took a colour: %q", line)
	}
}

// TestCostIsAnAbsenceRatherThanAGuess. A price is per model and comes from the
// catalog (design §10.2), which nothing fetches yet. The dash is deliberate: a
// hardcoded table would put a wrong number where the reader expects the bill,
// and this test is what a change replacing it has to argue with.
func TestCostIsAnAbsenceRatherThanAGuess(t *testing.T) {
	line := ansi.Strip(status{spent: llm.Usage{Input: 9000, Output: 4000}}.Render(0, styles.Dark))
	if !strings.HasSuffix(line, "cost —") {
		t.Errorf("the line does not end in an unknown cost: %q", line)
	}
}

func TestTokenCountsAreExactWhereItMattersAndRoundedWhereItDoesNot(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{n: 0, want: "0"},
		{n: 999, want: "999"},
		{n: 1000, want: "1k"},
		{n: 1049, want: "1k"},
		{n: 1234, want: "1.2k"},
		{n: 12_800, want: "12.8k"},
		{n: 999_949, want: "999.9k"},
		{n: 999_950, want: "1M"},
		{n: 1_000_000, want: "1M"},
		{n: 2_470_000, want: "2.5M"},
	} {
		if got := tokens(tc.n); got != tc.want {
			t.Errorf("tokens(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// usage is a finished reply carrying nothing but the counts the provider
// reported for it.
func usage(u llm.Usage) *llm.Message {
	return &llm.Message{Role: llm.RoleAssistant, Usage: u}
}

// counted renders exactly as a conversation item does and records how often it
// was asked to. A frame that reused a frozen string and one that rebuilt it are
// otherwise the same bytes, which is the whole thing under test.
type counted struct {
	chat.Message
	runs *int
}

func (c counted) Render(width int) string {
	*c.runs++
	return c.Message.Render(width)
}
