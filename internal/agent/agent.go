package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tool"
)

// DefaultMaxSteps is how many model calls one turn may make. It is a fuse and
// not a budget: a turn that reaches it has stopped making progress, and the
// number is far above anything a working turn needs (design §4).
const DefaultMaxSteps = 100

var (
	// ErrMaxSteps reports the fuse. Raising it is almost never the fix — the turn
	// was looping, and design §4 invariant 3 is what is supposed to notice.
	ErrMaxSteps = errors.New("the turn hit its step fuse")

	// ErrLooping reports a turn halted for repeating one tool call. It is not a
	// bound on work like the fuse above: the same call, with the same arguments,
	// answered the same way is the one shape that cannot be progress.
	ErrLooping = errors.New("the turn was repeating itself")

	// ErrInterrupted reports a turn that stopped because it was cancelled rather
	// than because the model finished. The transcript is left valid either way.
	ErrInterrupted = errors.New("the turn was interrupted before the model finished")

	// ErrTurnInProgress reports a second Send while one is running. Queueing it
	// would interleave two turns into one transcript; refusing says so.
	ErrTurnInProgress = errors.New("this agent is already running a turn")
)

// Config is what an agent needs to run a turn. Everything but MaxSteps and
// Events is required.
type Config struct {
	Provider llm.Provider

	// Tools is read once per turn through Snapshot. It may be mutated while a
	// turn runs; the running turn keeps the list it started with (design §3.3).
	Tools *tool.Registry

	// Model is the provider's own identifier, with no `provider/` prefix.
	Model string

	MaxTokens int

	// MaxSteps defaults to DefaultMaxSteps.
	MaxSteps int

	// Events receives every event a turn produces. A batch runs its tools
	// concurrently, so tool events come from as many goroutines as there are
	// calls in flight — but the agent holds a lock across this call, so a
	// consumer is never entered twice at once and needs no synchronisation of its
	// own. What it does need is to be quick: blocking in here stalls the turn,
	// and during a batch it stalls every other tool's events behind it. nil
	// discards them.
	Events func(Event)
}

// Agent runs turns against one provider and one transcript.
type Agent struct {
	provider  llm.Provider
	tools     *tool.Registry
	model     string
	maxTokens int
	maxSteps  int
	events    func(Event)

	mu      sync.Mutex
	running bool
	// messages is the transcript, guarded because Messages may be read from a
	// frontend's goroutine while the turn appends to it (design §6).
	messages []llm.Message

	// A lock of its own rather than mu, so a consumer may read Messages from
	// inside its callback without deadlocking against the turn that called it.
	eventsMu sync.Mutex
}

// New refuses a config that could not run a turn, rather than leaving it to fail
// at the first model call.
func New(cfg Config) (*Agent, error) {
	switch {
	case cfg.Provider == nil:
		return nil, errors.New("no provider, so there is nothing to call")
	case cfg.Tools == nil:
		return nil, errors.New("no tool registry; pass tool.NewRegistry(nil) for an agent the model cannot call tools from")
	case cfg.Model == "":
		return nil, errors.New("no model, and no provider picks one on our behalf")
	case cfg.MaxTokens <= 0:
		return nil, fmt.Errorf("the reply cap is %d, so there is nothing to ask for; "+
			"it comes from max_output_tokens in the configuration", cfg.MaxTokens)
	case cfg.MaxSteps < 0:
		return nil, fmt.Errorf("a step fuse of %d would end every turn before its first model call", cfg.MaxSteps)
	}

	steps := cfg.MaxSteps
	if steps == 0 {
		steps = DefaultMaxSteps
	}
	return &Agent{
		provider:  cfg.Provider,
		tools:     cfg.Tools,
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
		maxSteps:  steps,
		events:    cfg.Events,
	}, nil
}

// Send drives one turn: the user's message goes in, and the loop steps until the
// model stops asking for tools.
//
// nil means the turn is complete. A reply the output limit cut short counts as
// complete here and says so in its stop reason — whether a process reports that
// to a shell is a different question, and a different answer (decisions.md).
// Everything else ended the turn early: ErrInterrupted for a cancelled one,
// ErrMaxSteps for the fuse, ErrLooping for a turn that went in circles, and the
// provider's own error for a stream that failed. The transcript is left valid
// whichever way it ends, so the next Send can carry on from it.
func (a *Agent) Send(ctx context.Context, text string) error {
	if text == "" {
		// An assistant message with nothing sendable is a state; a user message
		// with nothing in it is a bug here, because rasp writes those
		// (decisions.md).
		return errors.New("the turn carries no prompt, and a user message with nothing in it is one every provider refuses")
	}
	if err := a.claim(); err != nil {
		return err
	}
	defer a.release()

	// One snapshot for the whole turn. The tool list sits inside the cached
	// prompt prefix, so a server connecting mid-turn must not change it under us
	// (design §3.3).
	t := &turn{agent: a, tools: a.tools.Snapshot()}

	a.append(llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.Block{{Type: llm.BlockText, Text: text}},
	})
	return t.run(ctx)
}

// Messages is the transcript so far.
func (a *Agent) Messages() []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.messages)
}

func (a *Agent) claim() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return ErrTurnInProgress
	}
	a.running = true
	return nil
}

func (a *Agent) release() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.running = false
}

func (a *Agent) append(msgs ...llm.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = append(a.messages, msgs...)
}

func (a *Agent) emit(ev Event) {
	if a.events == nil {
		return
	}
	a.eventsMu.Lock()
	defer a.eventsMu.Unlock()
	a.events(ev)
}
