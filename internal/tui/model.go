package tui

import (
	"context"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
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

	// messages is the conversation so far — the user's prompts and the
	// assistant's finished replies; streaming is the one still arriving, held
	// apart because it is replaced wholesale on every delta and committed only
	// when the step ends.
	messages  []llm.Message
	streaming *llm.Message

	calls []toolCall
	busy  bool
	err   error
}

func newModel(ctx context.Context, turner Turner) Model {
	return Model{ctx: ctx, turner: turner}
}

type toolCall struct {
	id     string
	name   string
	done   bool
	failed bool
}

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
		m.streaming, m.busy = ev.Message, true

	case agent.EventAssistantEnd:
		m.streaming = nil
		if ev.Message != nil {
			m.messages = append(m.messages, *ev.Message)
		}

	case agent.EventToolStart:
		m.calls = append(m.calls, toolCall{id: ev.CallID, name: ev.Tool})

	case agent.EventToolEnd:
		for i := range m.calls {
			if m.calls[i].id == ev.CallID {
				m.calls[i].done = true
				m.calls[i].failed = ev.Result != nil && ev.Result.IsError
			}
		}

	case agent.EventError:
		m = m.report(ev.Err)

	case agent.EventTurnEnd:
		m.busy = false
	}
	return m
}

func (m Model) View() tea.View {
	var b strings.Builder
	for _, msg := range m.messages {
		line := spoken(msg)
		if msg.Role == llm.RoleUser && line != "" {
			line = caret + line
		}
		writeLine(&b, line)
	}
	if m.streaming != nil {
		writeLine(&b, spoken(*m.streaming))
	}
	for _, call := range m.calls {
		writeLine(&b, call.line())
	}
	if m.err != nil {
		writeLine(&b, "error: "+m.err.Error())
	}
	if m.busy {
		writeLine(&b, "working…")
	}
	b.WriteString(caret + m.input)
	return tea.NewView(b.String())
}

// caret marks the input line and the messages sent from it alike, so a
// conversation of two speakers reads as one.
const caret = "› "

func (c toolCall) line() string {
	switch {
	case !c.done:
		return c.name + ": running"
	case c.failed:
		return c.name + ": failed"
	default:
		return c.name + ": done"
	}
}

func writeLine(b *strings.Builder, s string) {
	if s == "" {
		return
	}
	b.WriteString(s)
	b.WriteString("\n")
}

// spoken is the part of a message a reader is meant to see. Thinking is left out
// and tool blocks get a line of their own.
func spoken(msg llm.Message) string {
	var b strings.Builder
	for _, block := range msg.Content {
		if block.Type == llm.BlockText {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}
