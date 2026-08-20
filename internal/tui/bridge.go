package tui

import (
	"slices"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/permission"
)

// sender is the half of *tea.Program the pump uses: Send is the only entry into
// Update that is safe from another goroutine (design §6 rule 2).
type sender interface{ Send(tea.Msg) }

type agentMsg struct{ event agent.Event }

// mailbox is how many messages may wait for the UI before droppable applies.
const mailbox = 128

// bridge is the agent → UI seam: the turn's goroutine hands over an event or a
// permission request, one goroutine carries it to Program.Send, and nothing
// else crosses.
//
// The goroutine is the point. tea.Program's own mailbox is unbuffered, so Send
// blocks until Update takes the message — calling it from handle would stall the
// turn behind the render loop, and every other tool's events behind that
// (decisions.md, on the agent serialising its callback).
type bridge struct {
	msgs    chan tea.Msg
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func newBridge() *bridge {
	return &bridge{
		msgs:    make(chan tea.Msg, mailbox),
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

// handle is what agent.Config.Events receives, on the turn's own goroutine.
func (b *bridge) handle(ev agent.Event) {
	b.send(agentMsg{event: detach(ev)}, droppable(ev.Kind))
}

// prompt carries a permission request in, on the goroutine of the tool call
// that is blocked waiting for the answer. Never dropped: a question the mailbox
// discarded is a turn waiting on an answer to a question nobody was asked.
func (b *bridge) prompt(req permission.Request) {
	b.send(promptMsg{request: req}, false)
}

func (b *bridge) send(msg tea.Msg, lossy bool) {
	if lossy {
		select {
		case b.msgs <- msg:
		default:
		}
		return
	}
	select {
	case b.msgs <- msg:
	case <-b.done:
	}
}

func (b *bridge) start(s sender) {
	go func() {
		defer close(b.stopped)
		for {
			select {
			case msg := <-b.msgs:
				s.Send(msg)
			case <-b.done:
				return
			}
		}
	}()
}

// stop ends the pump and waits for it. It has to follow the program's own
// shutdown: the pump may be parked in Send, which returns only once the
// program's context is cancelled — and tea.Program.Run does that on its way out,
// whether it finished or failed.
func (b *bridge) stop() {
	b.once.Do(func() { close(b.done) })
	<-b.stopped
}

// droppable names the kinds a full mailbox may discard, and only the assistant
// delta qualifies: every event carries the whole accumulation, so the next delta
// says everything a dropped one would have (design §3.1). Everything else waits
// however slow the UI is — a turn whose end went missing leaves it busy for the
// rest of the session, with nothing later to correct it.
//
// A list of what may be lost rather than what may not, so a kind added later is
// carried losslessly until someone decides otherwise.
func droppable(kind agent.EventKind) bool { return kind == agent.EventAssistantDelta }

// detach copies the accumulation an event carries, because the message on a
// delta is the provider's own and is mutated in place for the rest of the step
// (design §3.1) — which is what makes internals §4.3's `m.current = ev.Partial`
// a race here. Per block rather than per byte: a Block holds a string header,
// and the provider replaces the string rather than writing through it.
func detach(ev agent.Event) agent.Event {
	if ev.Message == nil {
		return ev
	}
	msg := *ev.Message
	msg.Content = slices.Clone(msg.Content)
	ev.Message = &msg
	return ev
}
