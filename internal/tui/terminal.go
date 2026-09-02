package tui

import (
	"os"

	tea "charm.land/bubbletea/v2"
)

// windowTitle is the OSC 2 title text: rasp's own name beside the
// ~-abbreviated cwd place.go already computes for the footer, or empty for a
// session that named no cwd — which leaves View free to skip WindowTitle
// rather than draw the em dash on its own.
func (m Model) windowTitle() string {
	if m.status.path == "" {
		return ""
	}
	return "rasp — " + m.status.path
}

// bell is the completion signal's tea.Cmd, nil unless tty is true and ring is
// set — checked together rather than assumed, so a caller that wires one
// without the other still gets silence. Bubble Tea has no command for a raw
// control byte the way View carries one for the window title
// (View.WindowTitle, model.go); ring is the seam standing in for it, wired to
// stdout by Run and to whatever a test wants to count instead.
func (m Model) bell() tea.Cmd {
	if !m.tty || m.ring == nil {
		return nil
	}
	ring := m.ring
	return func() tea.Msg {
		ring()
		return nil
	}
}

// isTerminal is stdout as Run decides whether to reach for it: a character
// device, rather than a redirected file or pipe.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
