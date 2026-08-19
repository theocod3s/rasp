package tui

import (
	"strings"

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

	// messages is the assistant's finished replies; streaming is the one still
	// arriving, held apart because it is replaced wholesale on every delta and
	// committed only when the step ends.
	messages  []llm.Message
	streaming *llm.Message

	calls []toolCall
	busy  bool
	err   error
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
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case agentMsg:
		return m.apply(msg.event), nil
	}
	return m, nil
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
		m.err = ev.Err

	case agent.EventTurnEnd:
		m.busy = false
	}
	return m
}

func (m Model) View() tea.View {
	var b strings.Builder
	for _, msg := range m.messages {
		writeLine(&b, spoken(msg))
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
	return tea.NewView(b.String())
}

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
