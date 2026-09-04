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

	// inputHint names the key that breaks a line and the key that sends it,
	// because nothing else on the screen says a draft can hold more than one
	// line. It names Tab rather than shift+enter for the reason breaksLine binds
	// all three (model.go): Tab is the one every terminal delivers.
	inputHint = "⇥ newline · ⏎ send"

	// hintGap is the least space between the draft and the hint against the
	// right edge, so the two never read as one run.
	hintGap = 2
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

// typing is what sits inside the frame: the draft behind the caret, one line
// per line of it, or the placeholder while it is empty. The lines are returned
// as one string for View to write, so the frame's lower rule and the footer
// under it move down as the draft grows and back up as it shrinks.
//
// Nothing painted here says where the next keystroke lands. The terminal's own
// cursor is placed over that cell instead (cursor.go), which is what lets it
// blink — a cell painted into the frame cannot.
//
// The placeholder is cut to the terminal rather than left to wrap, which would
// grow the frame by a line for a string nobody is reading. What the user typed
// is not, because it is theirs.
func (m Model) typing() string {
	caret := m.caret()
	if m.input.empty() {
		line := caret + styles.For(m.background).Faint.Render(placeholder)
		if m.width > 0 && ansi.StringWidth(line) > m.width {
			line = ansi.Truncate(line, m.width, "")
		}
		return m.hinted(line)
	}

	lines := strings.Split(m.input.text, "\n")
	// Continuation lines are set in under the caret rather than against the left
	// margin, so a draft of several lines reads as one block the caret opens.
	indent := strings.Repeat(" ", ansi.StringWidth(caret))
	for i := range lines {
		if i == 0 {
			lines[i] = caret + lines[i]
			continue
		}
		lines[i] = indent + lines[i]
	}
	lines[len(lines)-1] = m.hinted(lines[len(lines)-1])
	return strings.Join(lines, "\n")
}

// hinted puts the draft's own hint against the right edge of its last line,
// and drops it whole the moment what is already on that line leaves no room —
// the order the activity line drops its own hint in (activity.go), and for
// the same reason: the advice is not what the reader came for.
//
// menuHint stands in for inputHint while the completion menu is open: Tab and
// Enter answer to it there, not to the line, and a hint still naming
// "newline" and "send" would be advice about keys that no longer do that.
func (m Model) hinted(line string) string {
	text := inputHint
	if m.menuOpen() {
		text = menuHint
	}
	used, hint := ansi.StringWidth(line), ansi.StringWidth(text)
	if m.width <= 0 || used+hintGap+hint > m.width {
		return line
	}
	return line + strings.Repeat(" ", m.width-used-hint) +
		styles.For(m.background).Faint.Render(text)
}
