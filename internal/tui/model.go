package tui

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tui/chat"
)

// Model is the root Bubble Tea model.
//
// Every field belongs to Update. View runs on the same goroutine but the pump
// and the turn do not, so a field written anywhere else is a data race against
// rendering — which is why the whole agent side arrives as a message and Update
// takes a value receiver (design §6 rule 1).
type Model struct {
	width, height int

	turner Turner

	// ctx is the program's, and every turn's context descends from it. Bubble Tea
	// answers a signal by ending the event loop rather than by calling Update, so
	// a turn interrupted that way is never offered the key that cancels it.
	ctx context.Context

	// cancel stops the running turn, and is nil when none is (design §6 rule 7).
	cancel context.CancelFunc

	// input is what the user has typed and not yet sent.
	input string

	// chat is the conversation and its render cache. Items land here as their
	// events arrive, which is what keeps a reply, the tools it asked for and the
	// next reply in the order they happened.
	chat chat.View

	// replies counts the assistant messages that have finished, and so names the
	// one still arriving: every delta of a step replaces the same item, and the
	// next step's deltas start a new one.
	replies int

	// streaming is the reply the current step is still receiving, kept because
	// the turn may end without one: an interrupted or failed step never reaches
	// its assistant_end (agent/step.go), and settle needs what the user is
	// already looking at.
	streaming *llm.Message

	// cards is every tool call drawn so far, by the provider's call id, so one
	// can be redrawn without the event that made it: an elapsed time that has
	// moved on, an expansion the user has just asked for. Shared between copies
	// of the model as chat.View's own map is, and for the same reason.
	cards map[string]card

	// expanded is the last thing the user asked for, and what a card added after
	// that inherits — so a call starting mid-turn arrives in the state the rest
	// of the conversation is already in.
	expanded bool

	// beating says a redraw of the running cards is already on its way. Four
	// calls starting at once would otherwise leave four timers running, each
	// rescheduling itself.
	beating bool

	// now is the model's clock. nil is time.Now; a test sets it to name the
	// times the frames it compares were drawn between.
	now func() time.Time

	busy bool
	err  error
}

// card is what the model needs to redraw one tool call: the item itself, and
// when the call started — which the item does not carry, because it renders an
// elapsed time rather than measuring one.
type card struct {
	item    chat.Call
	started time.Time
}

func newModel(ctx context.Context, turner Turner) Model {
	return Model{ctx: ctx, turner: turner}
}

func (m Model) clock() time.Time {
	if m.now == nil {
		return time.Now()
	}
	return m.now()
}

// replyKey and callKey are prefixed so that no provider call id can name an
// assistant message, whatever a server chooses to put in one.
func replyKey(n int) string    { return "reply/" + strconv.Itoa(n) }
func callKey(id string) string { return "call/" + id }

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		return m.press(msg)
	case agentMsg:
		return m.apply(msg.event)
	case tickMsg:
		return m.beat()
	case turnDone:
		return m.finish(msg), nil
	}
	return m, nil
}

// press matches on Code and Mod rather than on the key's printed name, so a
// binding is checked by the compiler instead of by a string that silently
// matches nothing.
func (m Model) press(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	key := msg.Key()
	switch {
	case key.Mod == tea.ModCtrl && key.Code == 'c':
		// Here rather than left to the program's exit, so the turn is already
		// unwinding while Bubble Tea shuts down.
		m.interrupt()
		return m, tea.Quit
	case key.Code == tea.KeyEscape:
		m.interrupt()
	case key.Code == tea.KeyEnter:
		return m.begin()
	case key.Mod == tea.ModCtrl && key.Code == 'r':
		return m.expand(), nil
	case key.Code == tea.KeyBackspace:
		m.input = backspace(m.input)
	default:
		m.input += key.Text
	}
	return m, nil
}

func backspace(s string) string {
	if s == "" {
		return s
	}
	_, width := utf8.DecodeLastRuneInString(s)
	return s[:len(s)-width]
}

func (m Model) apply(ev agent.Event) (Model, tea.Cmd) {
	switch ev.Kind {
	case agent.EventAssistantDelta:
		m.busy = true
		if ev.Message != nil {
			m.streaming = ev.Message
			m.chat.Set(replyKey(m.replies), chat.Message{Content: *ev.Message})
		}

	case agent.EventAssistantEnd:
		if ev.Message != nil {
			m.chat.Set(replyKey(m.replies), chat.Message{Content: *ev.Message, Done: true})
			m = m.announce(*ev.Message)
		}
		m.streaming = nil
		m.replies++

	case agent.EventToolStart:
		m = m.announced(ev.CallID, ev.Tool)
		c := m.cards[ev.CallID]
		c.started, c.item.State, c.item.Elapsed = m.clock(), chat.CallRunning, 0
		return m.draw(ev.CallID, c).pulse()

	case agent.EventToolEnd:
		m = m.announced(ev.CallID, ev.Tool)
		c := m.cards[ev.CallID]
		c.item.State, c.item.Result = chat.CallDone, ev.Result
		if !c.started.IsZero() {
			c.item.Elapsed = m.clock().Sub(c.started)
		}
		m = m.draw(ev.CallID, c)

	case agent.EventError:
		m = m.report(ev.Err)

	case agent.EventTurnEnd:
		m.busy = false
		m = m.settle()
	}
	return m, nil
}

// announce puts a card in the conversation for every call the step asked for,
// in the order the model asked for them. That order is not in the tool events
// themselves — a batch runs its calls at once, so tool_start arrives in
// whatever order they were scheduled — and it is on this message, which holds
// every tool_use block before the first of them runs (agent.Config.Events).
func (m Model) announce(msg llm.Message) Model {
	for _, block := range msg.Content {
		if block.Type == llm.BlockToolUse {
			m = m.announced(block.ID, block.Name)
		}
	}
	return m
}

// announced adds a queued card for a call the conversation has none for, so a
// tool event arriving without the assistant message that asked for it is drawn
// rather than dropped.
func (m Model) announced(id, name string) Model {
	if _, ok := m.cards[id]; ok {
		return m
	}
	if m.cards == nil {
		m.cards = make(map[string]card)
	}
	return m.draw(id, card{item: chat.Call{Name: name, Expanded: m.expanded}})
}

func (m Model) draw(id string, c card) Model {
	m.cards[id] = c
	m.chat.Set(callKey(id), c.item)
	return m
}

// expand shows or hides every card's body. The whole conversation rather than
// the card under a selection, because nothing selects one yet: the transcript
// has no cursor over it, so there is no "this card" to mean.
func (m Model) expand() Model {
	m.expanded = !m.expanded
	for id, c := range m.cards {
		c.item.Expanded = m.expanded
		m = m.draw(id, c)
	}
	return m
}

// tickInterval is how often a running call's elapsed time is redrawn, at the
// tenth of a second a card shows it to. Nothing else in the frame moves with
// it: every finished item is frozen (internals §4.5), so a beat re-renders the
// cards still running and hands back a stored string for everything else.
const tickInterval = 100 * time.Millisecond

// tickMsg carries no time. The model reads its own clock instead, so the
// elapsed time a card shows and the moment a call was recorded as starting come
// from one source rather than two that can disagree.
type tickMsg struct{}

func (m Model) pulse() (Model, tea.Cmd) {
	if m.beating {
		return m, nil
	}
	m.beating = true
	return m, tea.Tick(tickInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// beat moves the running cards on, and stops scheduling itself once none is
// running. A finished card is never written back here, so its render stays
// frozen at the text it ended on.
func (m Model) beat() (Model, tea.Cmd) {
	m.beating = false

	var running bool
	now := m.clock()
	for id, c := range m.cards {
		if c.item.State != chat.CallRunning {
			continue
		}
		running = true
		c.item.Elapsed = now.Sub(c.started)
		m = m.draw(id, c)
	}

	// Nothing running is the ordinary end of it. A card still running with no
	// turn behind it is one whose tool_end went missing, and beating on would
	// redraw it ten times a second for the rest of the session, timing a call
	// nothing is left to finish.
	if !running || !m.busy {
		return m, nil
	}
	return m.pulse()
}

// settle closes off a reply the turn ended in the middle of. Two things go
// wrong otherwise: the item is never finished, so every frame draws it again
// for the rest of the session, and its key stays live, so the next turn's first
// delta lands on it — drawing the new reply above the prompt that asked for it.
//
// On the turn's end and nowhere else. It is the last event a turn emits, where
// turnDone is a separate route into Update that can arrive before the events
// still in the pump; settling there would freeze a fragment and leave the real
// assistant_end to draw the reply a second time.
func (m Model) settle() Model {
	if m.streaming == nil {
		return m
	}
	m.chat.Set(replyKey(m.replies), chat.Message{Content: *m.streaming, Done: true})
	m.streaming = nil
	m.replies++
	return m
}

func (m Model) View() tea.View {
	var b strings.Builder
	b.WriteString(m.chat.Render(m.width))
	if m.err != nil {
		writeLine(&b, "error: "+m.err.Error())
	}
	if m.busy {
		writeLine(&b, "working…")
	}
	b.WriteString(chat.Caret + m.input)
	return tea.NewView(b.String())
}

func writeLine(b *strings.Builder, s string) {
	if s == "" {
		return
	}
	b.WriteString(s)
	b.WriteString("\n")
}
