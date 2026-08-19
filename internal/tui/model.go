package tui

import (
	"context"
	"strconv"
	"strings"
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

	busy bool
	err  error
}

func newModel(ctx context.Context, turner Turner) Model {
	return Model{ctx: ctx, turner: turner}
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
		return m.apply(msg.event), nil
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

func (m Model) apply(ev agent.Event) Model {
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
		}
		m.streaming = nil
		m.replies++

	case agent.EventToolStart:
		m.chat.Set(callKey(ev.CallID), chat.Call{Name: ev.Tool})

	case agent.EventToolEnd:
		m.chat.Set(callKey(ev.CallID), chat.Call{
			Name:   ev.Tool,
			Done:   true,
			Failed: ev.Result != nil && ev.Result.IsError,
		})

	case agent.EventError:
		m = m.report(ev.Err)

	case agent.EventTurnEnd:
		m.busy = false
		m = m.settle()
	}
	return m
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
