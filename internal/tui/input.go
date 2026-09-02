package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/tui/styles"
)

const (
	// placeholder is what an empty input line says, and the only invitation a
	// session that has just started gets.
	placeholder = "ask rasp anything…"

	// frameRule is one cell of the rules above and below that line. Faint rather
	// than muted: the frame is structure, and a reader who never notices it is
	// reading it correctly.
	frameRule = "─"
)

// rule is one edge of the input frame, at the terminal's full width. A terminal
// that has not reported a size has no width to draw across, and View drops the
// empty line rather than leaving a gap where the rule would be.
//
// Both edges take yolo's own inverse video while the bypass is armed (design
// §7.8) — the badge's own styling rather than a colour, so the border reads as
// loud on a terminal whose theme this build knows nothing about, the same
// reason the badge is drawn in it (status.go).
func (m Model) rule() string {
	if m.width <= 0 {
		return ""
	}
	line := strings.Repeat(frameRule, m.width)
	if m.status.yolo {
		return yoloStyle.Render(line)
	}
	return styles.For(m.background).Faint.Render(line)
}

// typing is the line inside the frame: what the user has behind the caret, or
// the placeholder while that is empty.
//
// The placeholder is cut to the terminal rather than left to wrap, which would
// grow the frame by a line for a string nobody is reading. What the user typed
// is not, because it is theirs.
func (m Model) typing() string {
	if m.input != "" {
		return m.caret() + m.input
	}
	line := m.caret() + styles.For(m.background).Faint.Render(placeholder)
	if m.width > 0 && ansi.StringWidth(line) > m.width {
		return ansi.Truncate(line, m.width, "")
	}
	return line
}
