package permission_test

import (
	"context"
	"errors"
	"strings"
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
			d := permission.DecisionOnce
			if i%2 == 1 {
				d = permission.DecisionReject
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
		if winner == permission.DecisionOnce && err != nil {
			t.Fatalf("the answer that won was %q but Ask = %v", winner, err)
		}
		if winner == permission.DecisionReject && !errors.Is(err, permission.ErrDenied) {
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

	if svc.Resolve(req.CallID, permission.DecisionAlways) {
		t.Errorf("an answer arriving after the prompt was abandoned reported deciding it")
	}
}

// TestAnAnswerThatBeatsACancellationKeepsItsGrant covers the user answering as
// the turn is cancelled: the grant has to survive whichever branch the select
// takes, because Resolve has already told the user their answer decided it.
func TestAnAnswerThatBeatsACancellationKeepsItsGrant(t *testing.T) {
	req := permission.Request{
		CallID: "call-1",
		Tool:   "write",
		Action: permission.ActionWrite,
		Path:   "/foo/a.go",
	}

	// Repeated because the branch is chosen at random: one run proves nothing
	// about the other half of the coin.
	for range 50 {
		h := newHarness(t, permission.DecisionAlways)

		ctx, cancel := context.WithCancel(t.Context())
		cancel() // both cases are ready by the time the select runs

		if err := h.Ask(ctx, req); err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Ask = %v, want the call allowed or the turn reported cancelled", err)
		}

		h.forget()
		h.answers(permission.DecisionReject)
		if err := h.Ask(t.Context(), req); err != nil {
			t.Fatalf("Ask after answering always = %v, want the grant to answer", err)
		}
	}
}

// TestTwoPromptsAreAnsweredIndependently pins the routing when two prompts are
// open at once. Nothing here stops that happening — the dispatcher is what keeps
// approvals one at a time (design §6 rule 5) — so this is what the service owes
// if a barrier upstream ever lets two through: an answer reaches the call it
// names, and a prompt closing does not disturb its siblings.
func TestTwoPromptsAreAnsweredIndependently(t *testing.T) {
	first := permission.Request{
		CallID: "call-1",
		Tool:   "write",
		Action: permission.ActionWrite,
		Path:   "/foo/a.go",
	}
	second := permission.Request{
		CallID:  "call-2",
		Tool:    "bash",
		Action:  permission.ActionExecute,
		Command: "go build ./...",
	}

	ui := &blocking{asked: make(chan permission.Request, 2)}
	svc := permission.New(ui)

	firstErr, secondErr := make(chan error, 1), make(chan error, 1)
	go func() { firstErr <- svc.Ask(t.Context(), first) }()
	go func() { secondErr <- svc.Ask(t.Context(), second) }()

	open := map[string]bool{}
	for range 2 {
		open[(<-ui.asked).CallID] = true
	}
	if !open[first.CallID] || !open[second.CallID] {
		t.Fatalf("prompts open for %v, want one for each call", open)
	}

	// Answered back to front, which is the order this exists to allow.
	if !svc.Resolve(second.CallID, permission.DecisionReject) {
		t.Fatalf("%s took no answer while its prompt was open", second.CallID)
	}
	if err := <-secondErr; !errors.Is(err, permission.ErrDenied) {
		t.Errorf("Ask for %s = %v, want the refusal it was answered with", second.CallID, err)
	}

	if !svc.Resolve(first.CallID, permission.DecisionOnce) {
		t.Fatalf("%s took no answer after its sibling closed", first.CallID)
	}
	if err := <-firstErr; err != nil {
		t.Errorf("Ask for %s = %v, want the yes it was answered with", first.CallID, err)
	}
}

// TestAnAnswerThePackageDoesNotKnowIsANoWithoutBlamingTheUser fails closed on a
// Decision that is none of the three, and asserts which denial it is.
func TestAnAnswerThePackageDoesNotKnowIsANoWithoutBlamingTheUser(t *testing.T) {
	req := permission.Request{
		CallID: "call-1",
		Tool:   "write",
		Action: permission.ActionWrite,
		Path:   "/foo/a.go",
	}

	ui := newBlocking()
	svc := permission.New(ui)

	asked := make(chan error, 1)
	go func() { asked <- svc.Ask(t.Context(), req) }()
	<-ui.asked

	if !svc.Resolve(req.CallID, permission.Decision("go on then")) {
		t.Fatalf("the prompt took no answer")
	}

	err := <-asked
	if !errors.Is(err, permission.ErrDenied) {
		t.Fatalf("Ask = %v, want an answer nobody recognises to fail closed", err)
	}
	if strings.Contains(err.Error(), "rejected") {
		t.Errorf("Ask = %v, want it not to report the user as having refused", err)
	}
}

// TestADenialNamesEverythingTheCallWasRefusedFor is what the model reads when a
// call is refused, and it is all it gets: a bash denial that named the command
// but not the file would send the model back at the same file.
func TestADenialNamesEverythingTheCallWasRefusedFor(t *testing.T) {
	// The command names the file the way the model wrote it and the path names it
	// the way the grant key holds it, so neither string contains the other — a
	// message that dropped one would otherwise still read as naming both.
	req := permission.Request{
		CallID:  "call-1",
		Tool:    "bash",
		Action:  permission.ActionExecute,
		Path:    "/work/internal/a.go",
		Command: "sed -i s/x/y/ internal/a.go",
	}

	h := newHarness(t, permission.DecisionReject)
	err := h.Ask(t.Context(), req)
	if err == nil {
		t.Fatalf("Ask = nil, want the refusal the user gave")
	}
	for _, want := range []string{req.Tool, req.Path, req.Command} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Ask = %v, want it to name %q", err, want)
		}
	}
}

// TestARequestWithNoCallIDIsNeverPublished keeps the id an answerable handle:
// two id-less requests would share one registry entry, and the second would be
// turned away over a prompt it cannot see.
func TestARequestWithNoCallIDIsNeverPublished(t *testing.T) {
	h := newHarness(t, permission.DecisionOnce)

	err := h.Ask(t.Context(), permission.Request{
		Tool:   "write",
		Action: permission.ActionWrite,
		Path:   "/foo/a.go",
	})
	if !errors.Is(err, permission.ErrDenied) {
		t.Errorf("Ask for a request with no call id = %v, want ErrDenied", err)
	}
	if len(h.prompts()) > 0 {
		t.Errorf("a request with no call id was put to the user anyway")
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

	h := newHarness(t, permission.DecisionOnce)
	if err := h.Ask(t.Context(), req); err != nil {
		t.Fatalf("Ask = %v, want the call allowed", err)
	}
	if h.Resolve(req.CallID, permission.DecisionAlways) {
		t.Errorf("answering an already-answered prompt reported deciding it")
	}
	if h.Resolve("a call that never asked", permission.DecisionOnce) {
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
	r.svc.Resolve(req.CallID, permission.DecisionOnce)
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
