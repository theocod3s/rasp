package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
)

// Program is a running rasp TUI: a Bubble Tea program, plus the one goroutine
// that carries agent events into it.
type Program struct {
	opts   []tea.ProgramOption
	bridge *bridge
}

// New builds the program. Options reach Bubble Tea unchanged, which is how a
// caller with no terminal — a test, a recorded session — supplies its own input
// and output.
func New(opts ...tea.ProgramOption) *Program {
	return &Program{opts: opts, bridge: newBridge()}
}

// Events is the sink for agent.Config.Events, and the only thing the agent knows
// about the UI. It is called on the turn's goroutine and returns quickly, except
// on a full mailbox, where everything but an assistant delta waits its turn.
func (p *Program) Events(ev agent.Event) { p.bridge.handle(ev) }

// Run draws until the user quits, and returns whatever ended it.
//
// The agent arrives here rather than at New because agent.Config.Events is fixed
// when the agent is built, and what it points at is this program's own sink.
func (p *Program) Run(a Turner) error {
	if a == nil {
		return errors.New("the program has no agent, so there would be nothing for a prompt to reach")
	}

	// Every turn's context descends from this one, so a program that stopped for
	// a reason Update never saw — a signal, a broken terminal — does not leave a
	// turn running against a UI that has gone.
	ctx, cancel := context.WithCancel(context.Background())

	prog := tea.NewProgram(newModel(ctx, a), p.opts...)
	p.bridge.start(prog)
	defer p.bridge.stop()
	defer cancel()

	_, err := prog.Run()
	return err
}
