package chat

import (
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// NoticeKind is which of the notice family's two voices a Notice speaks in.
type NoticeKind int

const (
	// NoticeInfo is rasp answering for itself — a command's reply, a mode
	// reminder — drawn faint since it is neither a prompt nor a reply and a
	// reader is meant to skim past it.
	NoticeInfo NoticeKind = iota

	// NoticeError is rasp reporting that a turn failed. Drawn in the palette's
	// own accent for "this went wrong" rather than faint: a failure is the one
	// thing this family says that a reader must not be able to skim past.
	NoticeError
)

// Notice is the UI answering for itself — what a slash command replied, what a
// command that cannot do its job yet says instead of doing nothing, and how a
// turn that failed is reported.
//
// An item of the conversation rather than a line of chrome, so the answer stays
// where it was asked. Drawn literally, never through the markdown renderer: it
// is neither a prompt nor a reply, and a reader must never have to work out
// which of rasp and the model said something.
type Notice struct {
	Text string
	Kind NoticeKind

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
	// Faint rather than Muted for the info voice: a mode reminder three lines
	// long drawn at the weight of information reads as part of what was said.
	style := styles.For(n.Background).Faint
	if n.Kind == NoticeError {
		style = styles.For(n.Background).Error
	}
	return paint(n.Text, style, width)
}
