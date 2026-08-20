package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tui/dialog"
)

// Permissions is the permission service as the UI reaches it: the answer to a
// question the service opened, and the mode the session is in. What an answer
// means stays behind it — this package renders and routes keys, and decides
// nothing (design §2).
type Permissions interface {
	// Resolve delivers the answer to the call that asked, and reports whether
	// this call is the one that decided it.
	Resolve(callID string, d permission.Decision) bool

	// SetMode installs the rules a mode stands for, and ends the yolo bypass if
	// one was armed. A mode with no rules to install fails rather than leaving
	// the session in one the ladder cannot read (mode.go).
	SetMode(mode permission.Mode) error

	// SetYolo arms or disarms the bypass that answers ahead of those rules. The
	// mode is left alone: disarming puts the session back under it (yolo.go).
	SetYolo(on bool)
}

// promptMsg carries a permission request from the goroutine that asked into
// Update, which is the only one that may touch the model (design §6 rule 1).
type promptMsg struct{ request permission.Request }

// promptGrace is how long after a question is drawn that a keystroke is
// swallowed rather than read as an answer. A question appears while the user
// may be mid-word and the keys that answer it are three ordinary letters, so
// without the pause the `a` of a sentence already being typed hands out a grant
// for the rest of the session.
const promptGrace = 500 * time.Millisecond

func promptKey(id string) string { return "ask/" + id }

func (m Model) asking() bool { return len(m.asked) > 0 }

func (m Model) ask(req permission.Request) Model {
	if m.permissions == nil {
		return m.say("A tool asked for approval and there is nothing wired up to answer it, so " +
			"the turn is stopped where it stands. Press esc twice to end it.")
	}
	m.asked = append(m.asked, req)
	if len(m.asked) == 1 {
		m = m.show()
	}
	return m
}

// show draws the question at the head of the queue and starts its grace period.
// Only the head: the three answer keys are the same for every question, so a
// second one drawn beside it would offer an answer that lands on the first. A
// batch puts its questions one at a time (agent/tools.go), which is what leaves
// this a queue nothing is expected to fill.
func (m Model) show() Model {
	req := m.asked[0]
	m.opened = m.clock()
	m.chat.Set(promptKey(req.CallID), dialog.Permission{Request: req, Background: m.background})
	return m
}

func (m Model) answer(key tea.Key) Model {
	if m.clock().Sub(m.opened) < promptGrace {
		return m
	}
	// Shift is the only modifier an answer may carry, because a capital letter
	// is still the letter. Ctrl+a is a reflex on any line editor and would
	// otherwise hand out a grant for the rest of the session.
	if key.Mod&^tea.ModShift != 0 {
		return m
	}
	decision, ok := dialog.Decide(key.Code)
	if !ok {
		return m
	}
	// The question closes whether or not the answer lands. One the service no
	// longer holds was cancelled with its turn and takes a late answer as a no-op
	// (permission/service.go); left on the screen it would keep the keyboard
	// pointed at a turn that has gone.
	m.permissions.Resolve(m.asked[0].CallID, decision)
	return m.dismiss()
}

func (m Model) dismiss() Model {
	req := m.asked[0]
	m.chat.Set(promptKey(req.CallID), dialog.Permission{
		Request:    req,
		Answered:   true,
		Background: m.background,
	})

	m.asked = m.asked[1:]
	if m.asking() {
		return m.show()
	}
	m.opened = time.Time{}
	return m
}

// dismissAll drops every question a turn left open. The turn's end is the only
// signal there is: a call abandoned mid-batch emits no tool events at all
// (agent/tools.go), and the Ask behind the question returned the moment its
// context did, so nothing is left to deliver an answer to.
func (m Model) dismissAll() Model {
	for m.asking() {
		m = m.dismiss()
	}
	return m
}
