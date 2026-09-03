package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/tui/chat"
)

// View draws the frame in two blocks — the conversation growing down from the
// top, the chrome held against the bottom edge — with blank lines between them
// while the two together are shorter than the terminal (gap, below).
func (m Model) View() tea.View {
	above, below := m.transcript(), m.chrome()

	v := tea.NewView(above + m.gap(above, below) + below)
	if m.tty {
		v.WindowTitle = m.windowTitle()
	}
	return v
}

// transcript is the conversation and the failure notice under it: what reads as
// history, and so stays with it when the gap opens underneath. A standing
// permission question is in here too — it is set into the conversation where it
// was asked (prompt.go), which is the whole of what makes it inline. Returned as
// chat.View rendered it rather than accumulated through a builder of its own,
// which cost the largest string the UI holds a second copy on every frame before
// View concatenated it (internals §4.5).
func (m Model) transcript() string {
	text := m.chat.Render(m.width)
	if m.err == nil {
		return text
	}

	// Blank first, when there is a transcript above to hold apart from — the same
	// one line of breathing room every item inside chat.View already gets
	// (chat/view.go), which this notice is drawn as though it were one of without
	// actually joining the conversation.
	blank := ""
	if text != "" {
		blank = "\n"
	}
	// Through the same notice path a command's own answer draws through, styled
	// in the accent that says "this went wrong" rather than as a bare line — the
	// one place left where an error was drawn in no colour at all. Joined in one
	// concatenation so the transcript is copied once here however this path goes.
	return text + blank + chat.Notice{
		Text:       "error: " + m.err.Error(),
		Kind:       chat.NoticeError,
		Background: m.background,
	}.Render(m.width) + "\n"
}

// chrome is what is held against the bottom of the screen. The activity line is
// in this block, and above the input frame rather than inside it, because it is
// about the turn running now: it belongs beside the keys that interrupt it, not
// at the end of a history the gap has moved away from them.
func (m Model) chrome() string {
	var b strings.Builder
	switch {
	case m.busy:
		writeLine(&b, m.activity(m.width))
	case m.quitArmed:
		// The one arm that outlives a turn: ctrl+c guards the session rather than
		// what is running in it, so its hint has to be drawable with no activity
		// line to hang off (model.go quitArmed).
		writeLine(&b, hintQuit)
	}

	rule := m.rule()
	writeLine(&b, rule)
	writeLine(&b, m.typing())
	writeLine(&b, m.menuView())
	writeLine(&b, m.queued())
	writeLine(&b, rule)
	b.WriteString(m.status.Render(m.width, m.background))
	return b.String()
}

// gap is the blank lines between the two blocks: as many as it takes for the
// frame to be exactly as tall as the terminal, and none once what is drawn
// already fills it.
//
// It is what makes a session's first frame cover the window: rasp draws inline
// rather than on the alternate screen, so a short frame leaves the shell's own
// output above it and a full-height one scrolls that into scrollback.
//
// Nothing is ever dropped to fit: past a screenful the frame is byte for byte
// what it would be unpadded, because the terminal's scrollback is the only
// history the conversation has, and lines trimmed here never reach it.
func (m Model) gap(above, below string) string {
	if m.leaving {
		return ""
	}
	// The lines the two blocks make once concatenated — logical lines, which are
	// rows too because the renderer cuts a line wider than the terminal rather
	// than wrapping it. A terminal that has not reported a size is zero high and
	// shorter than any frame, which is where this stands down (input.go).
	drawn := strings.Count(above, "\n") + strings.Count(below, "\n") + 1
	if drawn >= m.height {
		return ""
	}
	return strings.Repeat("\n", m.height-drawn)
}

func writeLine(b *strings.Builder, s string) {
	if s == "" {
		return
	}
	b.WriteString(s)
	b.WriteString("\n")
}
