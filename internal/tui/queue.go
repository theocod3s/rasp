package tui

import (
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/tui/styles"
)

// queueIndent sets a queued line in under its header, at the completion menu's
// own marker width (menu.go) so the frame keeps one gutter either way.
const queueIndent = "  "

// midTurn is whether a prompt sent now would reach an agent that has not
// finished the last one. Both fields, because the two routes out of a turn are
// unordered: EventTurnEnd puts busy down as soon as the loop stops, where
// cancel is cleared only by the turnDone that carries Send's own return value.
// In the gap between them a second prompt meets ErrTurnInProgress (agent.Send).
func (m Model) midTurn() bool { return m.busy || m.cancel != nil }

// enqueue clips before appending because the model is a value: two copies
// sharing one backing array would write a queued message over each other's.
func (m Model) enqueue() Model {
	text := strings.TrimSpace(m.input.text)
	if text == "" {
		return m
	}
	m.queue = append(slices.Clip(m.queue), text)
	m.input = draft{}
	return m.menuTracks()
}

// drain sends the message that has been waiting for the turn that just ended.
//
// Called from finish and nowhere else, which is what makes the queue safe:
// turnDone is one message per turn started and it carries Send's own return
// value, so the agent has already released. Draining on EventTurnEnd instead
// would fire while the agent is still inside Send, and draining on both would
// send two messages for one turn's end.
//
// A turn that failed or was interrupted holds the queue rather than sending
// into whatever went wrong: those messages were composed against a
// conversation that did not happen. They stay visible, in order, and the next
// turn that ends cleanly — one only the user can start — drains them.
func (m Model) drain(err error) (Model, tea.Cmd) {
	if len(m.queue) == 0 {
		return m, nil
	}
	if err != nil {
		return m.say(queueHeld(len(m.queue))), nil
	}
	text := m.queue[0]
	m.queue = m.queue[1:]
	return m.send(text)
}

// recalls is whether Up takes a queued message back rather than moving the
// caret. Only with the draft empty, where Up already does nothing at all
// (draft.go), so the binding costs no movement anyone can currently make.
func (m Model) recalls() bool { return m.input.empty() && len(m.queue) > 0 }

// recall takes the head of the queue — the one that sends next — back into the
// input, to be edited, sent as it stands, or emptied. That last one is the
// discard: nothing sends an empty draft.
//
// Sending it again mid-turn puts it at the back of the queue rather than the
// front it came from. The alternative is remembering where a draft came from
// through every edit, paste and command that can touch it.
func (m Model) recall() Model {
	text := m.queue[0]
	m.queue = m.queue[1:]
	m.input = draft{text: text, at: len(text)}
	return m
}

// queued draws the waiting messages inside the input frame rather than in the
// conversation: nothing here has been sent, and a transcript holding them would
// say otherwise.
func (m Model) queued() string {
	if len(m.queue) == 0 {
		return ""
	}

	faint := styles.For(m.background).Faint
	lines := make([]string, 0, len(m.queue)+1)
	lines = append(lines, menuLine(faint.Render(queueHeader(len(m.queue))), m.width))
	for _, text := range m.queue {
		lines = append(lines, menuLine(faint.Render(queueIndent+oneLine(text)), m.width))
	}
	return strings.Join(lines, "\n")
}

func queueHeader(n int) string {
	if n == 1 {
		return "1 queued · ↑ to edit it"
	}
	return strconv.Itoa(n) + " queued · ↑ to edit the first"
}

// queueHeld is said rather than left to the block above, which draws the same
// list whether the queue is about to send or holding — and that difference is
// the whole of what the user has to decide about.
func queueHeld(n int) string {
	if n == 1 {
		return "The turn ended early, so the message queued behind it was not sent. It is still " +
			"queued: ↑ takes it back into the input, and the next turn that finishes sends it."
	}
	return "The turn ended early, so the " + strconv.Itoa(n) + " messages queued behind it were not " +
		"sent. They are still queued, in order: ↑ takes the first back into the input, and the next " +
		"turn that finishes sends them."
}

// oneLine collapses a queued draft's whitespace, so a pasted diff takes one
// line of the frame rather than forty.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
