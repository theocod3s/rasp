package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/permission"
)

// Config is what the session is, as the UI has to say it: the model the agent
// was built against, and the mode it starts in. Both are resolved settings the
// UI only draws — a mode changed later is written here by Update and read by
// the permission service, never the other way round (design §7.4).
type Config struct {
	Model string
	Mode  permission.Mode
}

// Program is a running rasp TUI: a Bubble Tea program, plus the one goroutine
// that carries agent events into it.
type Program struct {
	cfg    Config
	opts   []tea.ProgramOption
	bridge *bridge
}

// New builds the program. Options reach Bubble Tea unchanged, which is how a
// caller with no terminal — a test, a recorded session — supplies its own input
// and output.
func New(cfg Config, opts ...tea.ProgramOption) *Program {
	return &Program{cfg: cfg, opts: opts, bridge: newBridge()}
}

// Events is the sink for agent.Config.Events, and the only thing the agent knows
// about the UI. It is called on the turn's goroutine and returns quickly, except
// on a full mailbox, where everything but an assistant delta waits its turn.
func (p *Program) Events(ev agent.Event) { p.bridge.handle(ev) }

// Prompt is permission.Prompter: it publishes a request the ladder could not
// answer from state and returns, on the goroutine of the tool call blocked on
// it (design §7.7 rung 5). The answer travels the other way, through the
// Permissions given to Run.
func (p *Program) Prompt(req permission.Request) { p.bridge.prompt(req) }

// Run draws until the user quits, and returns whatever ended it.
//
// The agent and the permission service arrive here rather than at New because
// each is built around something this program already owns: the agent around
// Events, and the service around Prompt. A nil Permissions is a session with no
// gate composed onto it — every tool runs, and a question arriving anyway is
// drawn as a notice rather than as an answerable prompt.
func (p *Program) Run(a Turner, answers Permissions) error {
	if a == nil {
		return errors.New("the program has no agent, so there would be nothing for a prompt to reach")
	}

	// Every turn's context descends from this one, so a program that stopped for
	// a reason Update never saw — a signal, a broken terminal — does not leave a
	// turn running against a UI that has gone.
	ctx, cancel := context.WithCancel(context.Background())

	m := newModel(ctx, a, p.cfg)
	m.permissions = answers

	prog := tea.NewProgram(m, p.opts...)
	p.bridge.start(prog)
	defer p.bridge.stop()

	final, err := prog.Run()

	// Not deferred: a deferred cancel runs after Wait below, which would then
	// block forever on the very turn this cancel is what stops.
	cancel()

	// Waited on because Bubble Tea's own shutdown does not (model.go turns) —
	// without this, Run can return while the turn ctrl+c just cancelled is
	// still committing what it has. No timeout: design §6 rule 7 threads the
	// context into everything a turn can be waiting on, so this is meant to
	// return as soon as that cancellation does — a bound here would let Run
	// exit before the commit again, on whichever call is the one that is slow.
	if m, ok := final.(Model); ok && m.turns != nil {
		m.turns.Wait()
	}
	return err
}
