package permission

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Decision is the user's answer, in the three shapes the prompt offers
// (prd §6.3): once, always for this session, no.
type Decision string

const (
	DecideOnce   Decision = "once"
	DecideAlways Decision = "always"
	DecideReject Decision = "reject"
)

// Rules answers rungs 1 and 2: what the active mode's preset, with the user's
// config merged onto it, says about a request. With none installed every request
// falls through to the rungs below, which is what an unmatched pattern means
// there anyway (design §7.3).
type Rules interface {
	Resolve(Request) Rule
}

// Prompter is the last rung. It publishes a request to whoever draws it and
// returns immediately; the answer comes back through Resolve, from whichever
// goroutine the user's keystroke lands on. Blocking inside Prompt holds the
// tool call that is already blocked on Ask, and no rung below can answer it.
type Prompter interface {
	Prompt(Request)
}

// ErrDenied is what Ask returns for every no: a mode that denies, a user who
// rejects, a prompt nobody can answer. A cancelled context is not one of them —
// Ask returns ctx.Err() there, so an interrupted turn is never a refusal.
var ErrDenied = errors.New("permission denied")

// Service answers whether a tool call may proceed, by walking design §7.7's
// ladder and prompting only when nothing above it has answered.
//
// Safe for concurrent use: a turn asks from every goroutine it dispatches a
// tool on, and the answer arrives from whichever goroutine read the keyboard.
type Service struct {
	// yolo is rung 0, loaded before any map is touched: a field rather than a
	// preset that allows everything, because a preset can be overridden into
	// denying and this cannot — under yolo no pattern is consulted at all. What
	// turns it on is not built yet; the ordering it depends on is.
	yolo atomic.Bool

	rules atomic.Pointer[Rules]

	prompter Prompter
	allowed  map[string]bool

	grants  sync.Map // grant → struct{}
	pending sync.Map // call id → *pending
}

// New returns a Service that prompts p when nothing above rung 5 has answered.
//
// allowed is rung 3, the config allow-list: a tool named there is allowed
// outright. It sits below the mode rules, so a mode that denies still denies —
// an allow-list widens what a mode would have asked about, it does not overrule
// what a mode refuses.
//
// A nil Prompter denies at rung 5: a request nobody can answer is a no, not a
// yes and not a wait.
func New(p Prompter, allowed ...string) *Service {
	s := &Service{prompter: p, allowed: make(map[string]bool, len(allowed))}
	for _, name := range allowed {
		s.allowed[name] = true
	}
	return s
}

// SetRules installs the compiled preset that answers rungs 1 and 2. Ask loads it
// at the moment of the check, so a mode switched mid-turn takes effect at the
// next check and is never retroactive: a tool already running finishes under the
// mode that approved it (design §7.4).
func (s *Service) SetRules(r Rules) {
	if r == nil {
		s.rules.Store(nil)
		return
	}
	s.rules.Store(&r)
}

// Ask walks the ladder and returns nil when the call may proceed. Every no is
// ErrDenied wrapped with what was refused; a cancelled or expired context comes
// back as ctx.Err() instead, so a turn the user interrupted is not mistaken for
// one the user refused.
func (s *Service) Ask(ctx context.Context, req Request) error {
	if s.yolo.Load() {
		return nil
	}

	switch s.resolve(req) {
	case RuleAllow:
		return nil
	case RuleDeny:
		return fmt.Errorf("%w: the active mode does not allow %s", ErrDenied, req)
	}

	if s.allowed[req.Tool] {
		return nil
	}
	if _, granted := s.grants.Load(req.grant()); granted {
		return nil
	}
	return s.ask(ctx, req)
}

func (s *Service) resolve(req Request) Rule {
	rules := s.rules.Load()
	if rules == nil {
		return RuleAsk
	}
	return (*rules).Resolve(req)
}

func (s *Service) ask(ctx context.Context, req Request) error {
	if s.prompter == nil {
		return fmt.Errorf("%w: there is nothing here that can ask about %s", ErrDenied, req)
	}

	p := &pending{reply: make(chan Decision, 1)}
	if _, open := s.pending.LoadOrStore(req.CallID, p); open {
		return fmt.Errorf("%w: call %s already has a prompt open, and an answer meant for "+
			"one of them cannot be told from an answer meant for the other", ErrDenied, req.CallID)
	}
	defer s.pending.Delete(req.CallID)

	s.prompter.Prompt(req)

	select {
	case d := <-p.reply:
		switch d {
		case DecideAlways:
			s.grants.Store(req.grant(), struct{}{})
			return nil
		case DecideOnce:
			return nil
		default:
			// Every answer that is not one of the two yeses is a no, so a
			// Decision this package has never heard of cannot approve anything.
			return fmt.Errorf("%w: the user rejected %s", ErrDenied, req)
		}
	case <-ctx.Done():
		p.abandon()
		return ctx.Err()
	}
}

// Resolve delivers an answer to the prompt open for callID and reports whether
// this call is the one that decided it. A second answer — a click landing behind
// the keystroke that already answered, or one arriving after the turn was
// cancelled — is a no-op rather than a second decision.
func (s *Service) Resolve(callID string, d Decision) bool {
	p, open := s.pending.Load(callID)
	if !open {
		return false
	}
	return p.(*pending).answer(d)
}

// pending is one open prompt. The reply channel is buffered so that the answer
// that wins never blocks on an asker who has already given up on a cancelled
// context — a UI goroutine parked on that would take the frontend with it.
type pending struct {
	once  sync.Once
	reply chan Decision
}

// answer delivers d and reports whether this call decided the prompt. sync.Once
// is what makes every later answer a no-op: the answers that race each other go
// through it, and so does abandon, which closes the prompt when the asker's
// context ends.
func (p *pending) answer(d Decision) bool {
	decided := false
	p.once.Do(func() {
		p.reply <- d
		decided = true
	})
	return decided
}

func (p *pending) abandon() { p.once.Do(func() {}) }
