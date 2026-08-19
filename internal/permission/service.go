package permission

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Decision is the user's answer, in the three shapes a prompt offers: once,
// always for this session, no.
type Decision string

const (
	DecisionOnce   Decision = "once"
	DecisionAlways Decision = "always"
	DecisionReject Decision = "reject"
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
	// denying and this cannot. Whatever comes to turn it on has to set it and
	// install the preset in one call (design §7.4) — two setters leave a window
	// where the bypass and the mode disagree about which is in force.
	yolo atomic.Bool

	rules atomic.Pointer[Rules]

	prompter Prompter
	allowed  map[string]bool

	// session counts the sessions this Service has served: an approval is
	// recorded only if the one that asked for it is still current. The counter
	// and the map are under a single lock so that check cannot be overtaken by
	// the clear it is checking for — an answer landing as a new session starts
	// would otherwise be stored just after it.
	mu      sync.Mutex
	session uint64
	grants  map[grant]bool

	pending sync.Map // call id → *pending
}

// New returns a Service that prompts p when nothing above rung 5 has answered.
//
// allowed is rung 3, the config allow-list. It sits below the mode rules, so an
// allow-list widens what a mode would have asked about and never overrules what
// a mode refuses. A name here allows every call that tool can make, so "bash" is
// every command — a rule about one command belongs in the mode's patterns, which
// are matched against the command line (design §7.3).
//
// A nil Prompter denies at rung 5: a request nobody can answer is a no, not a
// yes and not a wait.
func New(p Prompter, allowed ...string) *Service {
	s := &Service{
		prompter: p,
		allowed:  make(map[string]bool, len(allowed)),
		grants:   make(map[grant]bool),
	}
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

// ClearGrants drops every grant taken so far and ends the session they were
// given in, so an answer still on its way from a prompt open now records
// nothing. One process outlives a session, and an approval given in the one the
// user just left has no standing in the next.
func (s *Service) ClearGrants() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.session++
	clear(s.grants)
}

func (s *Service) granted(req Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.grants[req.grant()]
}

func (s *Service) currentSession() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.session
}

func (s *Service) remember(session uint64, req Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if session != s.session {
		return
	}
	s.grants[req.grant()] = true
}

// Ask walks the ladder and returns nil when the call may proceed. Every no is
// ErrDenied wrapped with what was refused. A context that ends while Ask is
// waiting on an answer comes back as ctx.Err() instead, so a turn the user
// interrupted is not mistaken for one the user refused; the rungs above the
// prompt answer from state alone and do not consult it.
func (s *Service) Ask(ctx context.Context, req Request) error {
	if s.yolo.Load() {
		return nil
	}

	switch rule := s.resolve(req); rule {
	case RuleAllow:
		return nil
	case RuleDeny:
		return fmt.Errorf("%w: the active mode does not allow %s", ErrDenied, req)
	case RuleAsk:
		// Down to the rungs below.
	default:
		// A rule the ladder cannot read is a broken preset, and two of the rungs
		// below it allow: falling through would turn a misspelled deny into a
		// call the allow-list waves past.
		return fmt.Errorf("%w: the active mode answers %q for %s, which is not a rule",
			ErrDenied, rule, req)
	}

	if s.allowed[req.Tool] {
		return nil
	}
	if s.granted(req) {
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
	// Refused rather than left to collide: two id-less requests would share one
	// entry below, and which of them was turned away would depend on whether the
	// batch happened to run them together.
	if req.CallID == "" {
		return fmt.Errorf("%w: %s carries no call id, so an answer could not be routed "+
			"back to it", ErrDenied, req)
	}

	session := s.currentSession()

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
		case DecisionAlways:
			s.remember(session, req)
			return nil
		case DecisionOnce:
			return nil
		case DecisionReject:
			return fmt.Errorf("%w: the user rejected %s", ErrDenied, req)
		default:
			// Fail closed, and say what happened rather than "the user rejected
			// it": the transcript would otherwise assert a refusal nobody made.
			return fmt.Errorf("%w: %s was answered %q, which is not one of the answers "+
				"a prompt takes", ErrDenied, req, d)
		}
	case <-ctx.Done():
		p.abandon()
		// An answer can win the race and still lose the select, which picks at
		// random between two ready cases. Its grant is honoured — the user did
		// answer, and Resolve has already told them so — while the turn is still
		// reported as cancelled, because it was. Nothing is missed: abandon's Do
		// blocks until an answer already inside the once has been delivered.
		select {
		case d := <-p.reply:
			if d == DecisionAlways {
				s.remember(session, req)
			}
		default:
		}
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

// pending is one open prompt. Its reply is buffered so the answer that wins
// never blocks on an asker who has already given up on a cancelled context — a
// UI goroutine parked there would take the frontend with it.
type pending struct {
	once  sync.Once
	reply chan Decision
}

// answer runs through the same sync.Once as abandon, which is what makes every
// answer after the first a no-op rather than a second decision.
func (p *pending) answer(d Decision) bool {
	decided := false
	p.once.Do(func() {
		p.reply <- d
		decided = true
	})
	return decided
}

func (p *pending) abandon() { p.once.Do(func() {}) }
