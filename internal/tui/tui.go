package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
)

// Program is a running rasp TUI: a Bubble Tea program, plus the one goroutine
// that carries agent events into it.
type Program struct {
	prog   *tea.Program
	bridge *bridge
}

// New builds the program. Options reach Bubble Tea unchanged, which is how a
// caller with no terminal — a test, a recorded session — supplies its own input
// and output.
func New(opts ...tea.ProgramOption) *Program {
	return &Program{prog: tea.NewProgram(Model{}, opts...), bridge: newBridge()}
}

// Events is the sink for agent.Config.Events, and the only thing the agent knows
// about the UI. It is called on the turn's goroutine and returns quickly, except
// on a full mailbox, where everything but an assistant delta waits its turn.
func (p *Program) Events(ev agent.Event) { p.bridge.handle(ev) }

// Run draws until the user quits, and returns whatever ended it.
func (p *Program) Run() error {
	p.bridge.start(p.prog)
	defer p.bridge.stop()
	_, err := p.prog.Run()
	return err
}
