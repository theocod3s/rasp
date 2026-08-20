package tui

import (
	"context"
	"errors"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tui/chat"
)

// Turner is the agent as the UI drives it. An interface rather than
// *agent.Agent, so a test needs a prompt and a reply and not a provider.
type Turner interface {
	Send(ctx context.Context, text string) error
}

type turnDone struct{ err error }

// begin submits what the user typed. The turn goes back to Bubble Tea as a
// tea.Cmd — its own goroutine, run after Update returns — because Send lasts as
// long as the model takes and Update is where every keystroke and every frame
// also has to be handled (design §6 rule 1, internals §4.3).
//
// The user's message is appended before the model has seen it, so the
// conversation shows the prompt on the keystroke that sent it.
func (m Model) begin() (Model, tea.Cmd) {
	text := strings.TrimSpace(m.input)
	if text == "" || m.busy {
		return m, nil
	}

	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	m.input = ""
	m.busy = true
	m.err = nil
	m.chat.Append(chat.Message{
		Content: llm.Message{
			Role:    llm.RoleUser,
			Content: []llm.Block{{Type: llm.BlockText, Text: text}},
		},
		Done: true,
	})

	if m.turns == nil {
		m.turns = &sync.WaitGroup{}
	}
	m.turns.Add(1)

	turner, turns := m.turner, m.turns
	return m, func() tea.Msg {
		defer turns.Done()
		return turnDone{err: turner.Send(ctx, text)}
	}
}

// interrupt cancels the running turn. The agent commits what arrived before it
// returns, so the transcript survives being stopped (design §4, decisions.md).
func (m Model) interrupt() {
	if m.cancel != nil {
		m.cancel()
	}
}

// finish takes what Send reported, which is separate from the EventTurnEnd that
// says what the transcript now holds.
func (m Model) finish(done turnDone) Model {
	m.interrupt()
	m.cancel = nil
	m.busy = false
	m.armed = false
	return m.report(done.err)
}

// report keeps a failure for the frame to draw — except an interruption, which
// the user caused and is watching. Send reports one anyway, because a caller
// that is not a person cannot tell a stopped turn from a finished one, and this
// is the one place with a user to stay quiet for (decisions.md).
//
// Both routes come through here: the same error also arrives as the EventError
// the loop emits before a failed turn's end (design §4), and silencing one alone
// draws the interruption.
func (m Model) report(err error) Model {
	if err != nil && !errors.Is(err, agent.ErrInterrupted) {
		m.err = err
	}
	return m
}
