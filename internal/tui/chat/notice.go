package chat

import (
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// Notice is the UI answering for itself — what a slash command replied, and
// what a command that cannot do its job yet says instead of doing nothing.
//
// An item of the conversation rather than a line of chrome, so the answer stays
// where it was asked. Drawn faint and literally: it is neither a prompt nor a
// reply, and a reader must never have to work out which of rasp and the model
// said something. Faint rather than Muted, which it was: a mode reminder three
// lines long drawn at the weight of information reads as part of what was said.
type Notice struct {
	Text string

	// Background is the terminal's, and picks the palette. Fixed when the notice
	// is made, as every appended item's is — the conversation can redraw only
	// what it can name, and a notice is not keyed.
	Background styles.Background
}

func (n Notice) Finished() bool { return true }

func (n Notice) Render(width int) string {
	if n.Text == "" {
		return ""
	}
	return paint(n.Text, styles.For(n.Background).Faint, width)
}
