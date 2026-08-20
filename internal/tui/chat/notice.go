package chat

import (
	"strings"

	"github.com/theocod3s/rasp/internal/tui/styles"
)

// Notice is the UI answering for itself — what a slash command replied, and
// what a command that cannot do its job yet says instead of doing nothing.
//
// An item of the conversation rather than a line of chrome, so the answer stays
// where it was asked. Drawn muted and literally: it is neither a prompt nor a
// reply, and a reader must never have to work out which of rasp and the model
// said something.
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
	// A line at a time: Lip Gloss renders a multi-line string as a block, padding
	// every line out to the longest with spaces the reader would find again on
	// the end of anything they copied.
	muted := styles.For(n.Background).Muted
	lines := strings.Split(wrap(n.Text, width), "\n")
	for i, line := range lines {
		lines[i] = muted.Render(line)
	}
	return strings.Join(lines, "\n")
}
