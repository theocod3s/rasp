package tui

import (
	"strconv"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tui/chat"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

const (
	hintInterrupt = "esc esc to interrupt"
	hintCancel    = "press esc again to cancel"
	hintQuit      = "press ctrl+c again to quit"
)

// activity is the line under the conversation while a turn runs: a spinner, the
// verb naming what the turn is doing, how long it has been going, and what a
// keypress would do about it.
//
// Drawn from the model's own fields rather than as an item in the conversation,
// for the reason the status line is (status.go): a frame in which only this
// moved re-renders nothing in the transcript (internals §4.5).
func (m Model) activity(width int) string {
	elapsed := m.clock().Sub(m.started)
	head := frame(elapsed) + " " + m.verb()
	if d := duration(elapsed); d != "" {
		head += " " + d
	}

	hint := m.hint()
	if width > 0 && ansi.StringWidth(head+statusSep+hint) > width {
		// The hint is what a narrow terminal spends its columns on last: esc esc
		// works whether or not the line has room to mention it, and which state
		// the turn is in is what nothing else on the screen says.
		if ansi.StringWidth(head) > width {
			return ansi.Truncate(head, width, "")
		}
		return head
	}
	return head + styles.For(m.background).Muted.Render(statusSep+hint)
}

// frame is the spinner glyph for a turn this long. Read off the clock rather
// than counted per tick, so a tick that arrived late or twice does not put the
// animation somewhere else — and a test with a fixed clock draws one frame.
func frame(elapsed time.Duration) string {
	if elapsed < 0 {
		return styles.Spinner[0]
	}
	return styles.Spinner[int(elapsed/tickInterval)%len(styles.Spinner)]
}

// duration is the turn's elapsed time as this line says it, and nothing for a
// turn too short to have one — at the threshold a card uses, so the two start
// reading together.
func duration(d time.Duration) string {
	if r := d.Round(tickInterval); r > 0 {
		return r.String()
	}
	return ""
}

// verb names what the turn is doing, and a running call outranks everything
// else: the line must never say thinking while a tool runs, because it is the
// only window the reader has onto which of the two is true.
func (m Model) verb() string {
	switch n, name := m.inFlight(); {
	case n == 1:
		return "running " + name
	case n > 1:
		return "running " + strconv.Itoa(n) + " tools"
	}

	switch m.lastBlock() {
	case llm.BlockThinking:
		return "thinking"
	case llm.BlockText:
		return "writing"
	}
	// Between two steps, and while a call's arguments are still streaming: the
	// model is neither reasoning aloud nor writing a reply, and nothing has been
	// dispatched yet for the line to name.
	return "working"
}

// inFlight is how many calls are running and, when that is one, which. The name
// means nothing for a batch — the cards are a map, so it is whichever the range
// reached last — which is why the count decides what gets drawn.
func (m Model) inFlight() (int, string) {
	var (
		n    int
		name string
	)
	for _, c := range m.cards {
		if c.item.State == chat.CallRunning {
			n, name = n+1, c.item.Name
		}
	}
	return n, name
}

// lastBlock is the kind of block the step is filling right now, and empty when
// no reply is arriving. The last rather than a search for a kind: a step that
// thought and then started writing is writing, not both.
func (m Model) lastBlock() llm.BlockType {
	if m.streaming == nil || len(m.streaming.Content) == 0 {
		return ""
	}
	return m.streaming.Content[len(m.streaming.Content)-1].Type
}

func (m Model) hint() string {
	switch {
	case m.armed:
		return hintCancel
	case m.quitArmed:
		return hintQuit
	}
	return hintInterrupt
}
