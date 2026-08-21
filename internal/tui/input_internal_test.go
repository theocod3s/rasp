package tui

import (
	"errors"
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

// TestTheInputSitsInsideItsOwnFrame. The line the user types on is the one part
// of the screen that is theirs, and the rules are what separate it from a
// conversation that has been scrolling past it.
func TestTheInputSitsInsideItsOwnFrame(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	m.width = goldenWidth

	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	i := caretLine(t, lines)

	if i == 0 || i == len(lines)-1 {
		t.Fatalf("the input is line %d of %d, so it has no room for a frame:\n%s", i, len(lines),
			strings.Join(lines, "\n"))
	}
	for _, edge := range []int{i - 1, i + 1} {
		rule := strings.Repeat(frameRule, goldenWidth)
		if lines[edge] != rule {
			t.Errorf("line %d reads %q, want the frame's rule", edge, lines[edge])
		}
	}
	// And the footer under it rather than above: the frame is the bottom of the
	// conversation, and what the session *is* goes below the whole of it.
	if last := lines[len(lines)-1]; !strings.Contains(last, string(permission.ModeManual)) {
		t.Errorf("the last line of the frame is %q, and the footer is what ends it", last)
	}
}

// TestATerminalOfUnknownWidthDrawsNoRule. A rule is drawn across the terminal,
// and a terminal that has not said how wide it is has nothing to draw across —
// a fixed guess would be a line of the wrong length on the first frame of every
// session.
func TestATerminalOfUnknownWidthDrawsNoRule(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})

	frame := m.View().Content
	if strings.Contains(frame, frameRule) {
		t.Errorf("a frame drawn before the terminal reported a size carries a rule:\n%s", frame)
	}
	if !strings.Contains(frame, chat.Caret) {
		t.Errorf("the input line is gone with the frame around it:\n%s", frame)
	}
}

// TestTheEmptyLineAsks. An empty input line says nothing about what to do with
// it, and this is the first thing a new session shows anyone.
func TestTheEmptyLineAsks(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	m.width = goldenWidth

	line := typedLineOf(t, m)
	if !strings.HasSuffix(line, placeholder) {
		t.Errorf("the empty input line reads %q, and the invitation is not on it", line)
	}
	// Faint, not plain: it is a prompt to the user rather than something they
	// wrote, and a placeholder at full contrast reads as text already typed.
	if !strings.Contains(m.typing(), styleOf(m, placeholder)) {
		t.Errorf("the placeholder is drawn at full contrast: %q", m.typing())
	}

	m = typed(m, "fix the auth test")

	line = typedLineOf(t, m)
	if strings.Contains(line, placeholder) {
		t.Errorf("the invitation is still on a line that has been typed on: %q", line)
	}
	if !strings.HasSuffix(line, "fix the auth test") {
		t.Errorf("the input line reads %q, and what was typed is not on it", line)
	}
}

// TestTheFrameHoldsWhileAQuestionStands. A permission question takes the
// keyboard off the input line (prompt.go) — it must not take the line itself,
// or answering one would move everything on the screen.
func TestTheFrameHoldsWhileAQuestionStands(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	m.permissions = &answers{decided: true}
	m.width = goldenWidth

	before := strings.Count(ansi.Strip(m.View().Content), strings.Repeat(frameRule, goldenWidth))
	m = m.ask(permission.Request{CallID: "call_1", Tool: "edit", Action: permission.ActionEdit, Path: "auth.go"})

	frame := ansi.Strip(m.View().Content)
	if !strings.Contains(frame, "needs your approval") {
		t.Fatalf("the question was never drawn, so nothing here is about a frame under one:\n%s", frame)
	}
	if after := strings.Count(frame, strings.Repeat(frameRule, goldenWidth)); after != before {
		t.Errorf("the frame has %d rule(s) with a question open and %d without one:\n%s", after, before, frame)
	}
	if line := typedLineOf(t, m); !strings.HasSuffix(line, placeholder) {
		t.Errorf("the input line reads %q while a question stands", line)
	}
}

// TestTypingRedrawsNoPartOfTheConversation is the no-re-render guarantee over
// the bottom chrome (internals §4.5): the frame, the line inside it and the
// footer are drawn from the model's own fields, so a keystroke costs no item
// render at all — which is what keeps a long transcript from being rebuilt on
// every letter of a prompt.
func TestTypingRedrawsNoPartOfTheConversation(t *testing.T) {
	var runs int
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
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

	for _, text := range []string{"fix", " the", " auth test"} {
		m = typed(m, text)
		frames = append(frames, m.View().Content)
	}

	for i := 1; i < len(frames); i++ {
		if frames[i] == frames[i-1] {
			t.Fatalf("frame %d is the one before it, so nothing about the input moved and the count "+
				"below is about a UI that drew nothing:\n%s", i, frames[i])
		}
	}
	if redrawn := runs - warm; redrawn != 0 {
		t.Errorf("%d conversation item render(s) across %d frames in which only what was typed changed",
			redrawn, len(frames)-1)
	}
}

// TestTheInvitationIsCutToTheTerminal. It is the UI's own words rather than the
// user's, so a terminal too narrow for it takes as much as fits: wrapped, it
// would push the frame's lower rule down a line and take the footer off a short
// screen with it.
func TestTheInvitationIsCutToTheTerminal(t *testing.T) {
	const narrow = 8

	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	m = update(m, tea.WindowSizeMsg{Width: narrow, Height: goldenHeight})

	for _, line := range strings.Split(m.View().Content, "\n") {
		if n := ansi.StringWidth(line); n > narrow {
			t.Errorf("a line runs %d columns into a terminal %d wide: %q", n, narrow, line)
		}
	}
}

// TestTheBottomChromeIsOnEveryFrame walks the states a turn passes through and
// checks the frame is still under all of them. The states are the ones that
// draw something of their own between the conversation and the input — an
// activity line, an error — where a chrome appended in the wrong order would
// leave the input somewhere other than the bottom.
func TestTheBottomChromeIsOnEveryFrame(t *testing.T) {
	asked := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockText, Text: "Reading it."},
		{Type: llm.BlockToolUse, ID: "call_1", Name: "read"},
	}}

	for _, tc := range []struct {
		name   string
		events []agent.Event
	}{
		{name: "nothing has happened yet"},
		{name: "a tool is running", events: []agent.Event{
			{Kind: agent.EventAssistantEnd, Message: asked},
			{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"},
		}},
		{name: "the turn failed", events: []agent.Event{
			{Kind: agent.EventError, Err: errors.New("the provider closed the stream mid-message")},
			{Kind: agent.EventTurnEnd},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
			m = update(m, tea.WindowSizeMsg{Width: goldenWidth, Height: goldenHeight})
			for _, ev := range tc.events {
				m = update(m, agentMsg{event: ev})
			}

			lines := strings.Split(ansi.Strip(m.View().Content), "\n")
			if i := caretLine(t, lines); i != len(lines)-3 {
				t.Errorf("the input is line %d of %d, and the rule and the footer are what follow it:\n%s",
					i, len(lines), strings.Join(lines, "\n"))
			}
		})
	}
}

// caretLine is where in the frame the user types: the last line carrying a
// caret, since every prompt in the conversation above carries one too.
func caretLine(t *testing.T, lines []string) int {
	t.Helper()

	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimPrefix(lines[i], yoloCaret+" "), chat.Caret) {
			return i
		}
	}
	t.Fatalf("no line in the frame is the one being typed on:\n%s", strings.Join(lines, "\n"))
	return -1
}

// typedLineOf is that line, off a whole frame.
func typedLineOf(t *testing.T, m Model) string {
	t.Helper()

	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	return lines[caretLine(t, lines)]
}

// styleOf is text as this model's palette draws something faint, which is what
// a placeholder has to be rendered in to read as one.
func styleOf(m Model, text string) string {
	return styles.For(m.background).Faint.Render(text)
}
