package permission_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/theocod3s/rasp/internal/permission"
)

// blocking publishes a prompt and answers nothing, which is what the UI looks
// like between drawing the prompt and the user reaching the keyboard.
type blocking struct{ asked chan permission.Request }

func newBlocking() *blocking { return &blocking{asked: make(chan permission.Request, 1)} }

func (b *blocking) Prompt(req permission.Request) { b.asked <- req }

// TestOnlyTheFirstAnswerDecidesAPrompt covers the race the prompt overlay makes
// reachable: a keystroke, a click and a cancelled turn can all answer at once.
// Exactly one of them decides, the rest are no-ops, and the decision the turn
// acts on is the winner's — a prompt that took two answers would allow a call
// the user rejected.
func TestOnlyTheFirstAnswerDecidesAPrompt(t *testing.T) {
	const resolvers = 8

	req := permission.Request{
		CallID:  "call-1",
		Tool:    "bash",
		Action:  permission.ActionExecute,
		Command: "rm -rf dist",
	}

	// Repeated, because a lock held a moment too long passes this once out of
	// habit and fails under -race across a few dozen attempts.
	for range 50 {
		ui := newBlocking()
		svc := permission.New(ui)

		asked := make(chan error, 1)
		go func() { asked <- svc.Ask(t.Context(), req) }()
		<-ui.asked

		type outcome struct {
			decision permission.Decision
			decided  bool
		}
		outcomes := make([]outcome, resolvers)

		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range resolvers {
			d := permission.DecideOnce
			if i%2 == 1 {
				d = permission.DecideReject
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				outcomes[i] = outcome{decision: d, decided: svc.Resolve(req.CallID, d)}
			}()
		}
		close(start)
		wg.Wait()

		var winner permission.Decision
		decided := 0
		for _, o := range outcomes {
			if o.decided {
				decided++
				winner = o.decision
			}
		}
		if decided != 1 {
			t.Fatalf("%d of %d answers reported deciding the prompt, want exactly 1", decided, resolvers)
		}

		err := <-asked
		if winner == permission.DecideOnce && err != nil {
			t.Fatalf("the answer that won was %q but Ask = %v", winner, err)
		}
		if winner == permission.DecideReject && !errors.Is(err, permission.ErrDenied) {
			t.Fatalf("the answer that won was %q but Ask = %v", winner, err)
		}
	}
}

// TestAPendingPromptEndsWithTheRequestsContext is what makes an interrupt work
// while a prompt is open: the turn's context ends the wait, and the turn learns
// it was cancelled rather than refused.
func TestAPendingPromptEndsWithTheRequestsContext(t *testing.T) {
	req := permission.Request{
		CallID: "call-1",
		Tool:   "write",
		Action: permission.ActionWrite,
		Path:   "/foo/a.go",
	}

	ui := newBlocking()
	svc := permission.New(ui)

	ctx, cancel := context.WithCancel(t.Context())
	asked := make(chan error, 1)
	go func() { asked <- svc.Ask(ctx, req) }()
	<-ui.asked

	cancel()
	err := <-asked
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Ask on a cancelled context = %v, want context.Canceled", err)
	}
	if errors.Is(err, permission.ErrDenied) {
		t.Errorf("Ask reported the cancelled turn as a refusal: %v", err)
	}

	if svc.Resolve(req.CallID, permission.DecideAlways) {
		t.Errorf("an answer arriving after the prompt was abandoned reported deciding it")
	}
}

// TestAnswerToAPromptThatIsNotOpenIsANoOp covers the ids a UI can hold that the
// service no longer has: an answered prompt, and one from a previous turn.
func TestAnswerToAPromptThatIsNotOpenIsANoOp(t *testing.T) {
	req := permission.Request{
		CallID: "call-1",
		Tool:   "write",
		Action: permission.ActionWrite,
		Path:   "/foo/a.go",
	}

	h := newHarness(permission.DecideOnce)
	if err := h.Ask(t.Context(), req); err != nil {
		t.Fatalf("Ask = %v, want the call allowed", err)
	}
	if h.Resolve(req.CallID, permission.DecideAlways) {
		t.Errorf("answering an already-answered prompt reported deciding it")
	}
	if h.Resolve("a call that never asked", permission.DecideOnce) {
		t.Errorf("answering a prompt that was never opened reported deciding it")
	}
}

// TestAskWithNobodyToAskIsADenial is the shape of every check in this repo that
// cannot run: a request that reaches rung 5 with no Prompter is refused, not
// allowed and not left waiting for an answer that has no source.
func TestAskWithNobodyToAskIsADenial(t *testing.T) {
	svc := permission.New(nil)

	err := svc.Ask(t.Context(), permission.Request{
		CallID: "call-1",
		Tool:   "write",
		Action: permission.ActionWrite,
		Path:   "/foo/a.go",
	})
	if !errors.Is(err, permission.ErrDenied) {
		t.Errorf("Ask with no Prompter = %v, want ErrDenied", err)
	}
}

// reentrant asks a second time for the same call from inside the first prompt,
// which is what a caller that retried an Ask would do to the service.
type reentrant struct {
	svc   *permission.Service
	ctx   context.Context
	depth int
	inner error
}

func (r *reentrant) Prompt(req permission.Request) {
	r.depth++
	if r.depth == 1 {
		r.inner = r.svc.Ask(r.ctx, req)
	}
	r.svc.Resolve(req.CallID, permission.DecideOnce)
}

// TestASecondPromptForOneCallIsRefused keeps the call id an unambiguous handle.
// Two prompts open under one id and an answer cannot be routed to the request
// that earned it, so the second Ask is refused rather than joining the queue.
func TestASecondPromptForOneCallIsRefused(t *testing.T) {
	ui := &reentrant{ctx: t.Context()}
	ui.svc = permission.New(ui)

	req := permission.Request{
		CallID: "call-1",
		Tool:   "write",
		Action: permission.ActionWrite,
		Path:   "/foo/a.go",
	}
	if err := ui.svc.Ask(t.Context(), req); err != nil {
		t.Fatalf("Ask = %v, want the call allowed", err)
	}
	if !errors.Is(ui.inner, permission.ErrDenied) {
		t.Errorf("the second Ask for call %s = %v, want ErrDenied", req.CallID, ui.inner)
	}
	if ui.depth != 1 {
		t.Errorf("the user was prompted %d times for one call, want 1", ui.depth)
	}
}
