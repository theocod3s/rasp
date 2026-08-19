package permission_test

import (
	"slices"
	"sync"
	"testing"

	"go.uber.org/goleak"

	"github.com/theocod3s/rasp/internal/permission"
)

// TestMain runs the leak detector over the package. An unanswered prompt parks
// the goroutine that asked, and one still parked at the end of a turn is the
// hang design §13 puts this check here to catch.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// rules answers rungs 1 and 2 from a function.
type rules func(permission.Request) permission.Rule

func (f rules) Resolve(req permission.Request) permission.Rule { return f(req) }

func fixed(r permission.Rule) rules {
	return func(permission.Request) permission.Rule { return r }
}

// harness is a Service and the Prompter it asks, answering inline with a fixed
// decision. Answering from inside Prompt keeps the ladder tests free of
// scheduling: Ask registers the prompt before it publishes, and the reply
// channel is buffered, so an answer given this early is waiting when Ask arrives
// for it.
type harness struct {
	*permission.Service

	mu     sync.Mutex
	answer permission.Decision
	asked  []permission.Request
}

func newHarness(answer permission.Decision, allowed ...string) *harness {
	h := &harness{answer: answer}
	h.Service = permission.New(h, allowed...)
	return h
}

func (h *harness) Prompt(req permission.Request) {
	h.mu.Lock()
	h.asked = append(h.asked, req)
	answer := h.answer
	h.mu.Unlock()

	if answer != "" {
		h.Resolve(req.CallID, answer)
	}
}

func (h *harness) prompts() []permission.Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.asked)
}

func (h *harness) forget() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked = nil
}

// answers installs a decision and returns the one it replaced.
func (h *harness) answers(d permission.Decision) permission.Decision {
	h.mu.Lock()
	defer h.mu.Unlock()
	was := h.answer
	h.answer = d
	return was
}

// grantOnce answers one prompt with "always", leaving a session grant behind and
// the harness otherwise as it was.
func grantOnce(t *testing.T, h *harness, req permission.Request) {
	t.Helper()

	was := h.answers(permission.DecideAlways)
	if err := h.Ask(t.Context(), req); err != nil {
		t.Fatalf("granting %v: %v", req, err)
	}
	// A grant is recorded by answering a prompt, so a setup that was never
	// prompted has quietly recorded nothing, and every assertion resting on it
	// would pass for the wrong reason.
	if len(h.prompts()) == 0 {
		t.Fatalf("granting %v: nothing prompted, so no grant exists to test against", req)
	}
	h.forget()
	h.answers(was)
}
