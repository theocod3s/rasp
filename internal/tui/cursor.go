package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// cursor is where the terminal puts its own cursor, and nil for a frame that
// must not carry one — nil is how the renderer is told to hide it. Like the
// window title (terminal.go) it travels on tea.View rather than inside Content,
// so placing it moves no byte of the frame and no recorded golden with it.
//
// under is how many rows of the frame sit below the input line (frame.go).
func (m Model) cursor(content string, under int) *tea.Cursor {
	// A cursor is a promise about the next keystroke, so it goes wherever that
	// promise is false: a standing question and the completion menu each take the
	// keyboard off the input line (prompt.go, menu.go), and the parting frame is
	// one the session has already left.
	if m.asking() || m.menuOpen() || m.leaving {
		return nil
	}

	// Counted up from the bottom, because both the top of the frame and its
	// height move: the padding above the chrome is as many blank lines as it
	// takes to fill the terminal (frame.go gap), and a frame taller than the
	// screen is drawn with its first rows already scrolled off — so the last row
	// there is to stand on is the shorter of frame and screen.
	rows := strings.Count(content, "\n") + 1
	if m.height > 0 && m.height < rows {
		rows = m.height
	}
	row := rows - 1 - under - m.input.below()
	if row < 0 {
		// A terminal too short for the chrome under the draft. Hidden rather than
		// clamped: a cursor held at the top edge would blink on somebody else's line.
		return nil
	}
	return tea.NewCursor(m.caretColumn(), row)
}

// caretColumn is the cell the cursor stands in: the caret the line opens with,
// plus whatever is typed in front of the draft's own caret on that line.
//
// Cells rather than runes — ansi.StringWidth, the measure the rules and the
// hints are already laid out in. A column counted in runes sits one cell left of
// the caret for every CJK character or emoji typed before it.
func (m Model) caretColumn() int {
	col := ansi.StringWidth(m.caret()) + ansi.StringWidth(m.input.prefix())
	// A draft is never cut to the terminal, because it is the user's (input.go),
	// so a line longer than the screen runs past the last column there is.
	if m.width > 0 && col >= m.width {
		return m.width - 1
	}
	return col
}
