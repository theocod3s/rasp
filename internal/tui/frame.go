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
	above := m.transcript()
	below, under := m.chrome()

	content := above + m.gap(above, below) + below
	v := tea.NewView(content)
	v.Cursor = m.cursor(content, under)
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

// chrome is what is held against the bottom of the screen, and how many of its
// rows sit under the line being typed on. The activity line is in this block,
// and above the input frame rather than inside it, because it is about the turn
// running now: it belongs beside the keys that interrupt it, not at the end of a
// history the gap has moved away from them.
//
// The count is taken as the block is built rather than measured off the frame
// afterward, because this is where those rows are decided. It is the distance
// the terminal's cursor is placed up from the bottom edge, which is the one end
// of the frame the padding above cannot move (cursor.go).
func (m Model) chrome() (block string, under int) {
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

	menu, queued := m.menuView(), m.queued()
	footer := m.status.Render(m.width, m.background)
	writeLine(&b, menu)
	writeLine(&b, queued)
	writeLine(&b, rule)
	b.WriteString(footer)

	return b.String(), rows(menu) + rows(queued) + rows(rule) + rows(footer)
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

// rows is how many lines of the frame s takes, and none for the empty string
// writeLine drops rather than writing as a blank line.
func rows(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
