package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/fake"
	"github.com/theocod3s/rasp/internal/tool"
)

// gate is an Approver scripted by tool name: a name in asks is one whose
// approval puts a question to the user, and a name in refuses is one the answer
// is no for. The two are independent, because a mode that denies refuses without
// asking anyone.
//
// blind drops the hint Prompts gives the dispatcher without changing what
// Approve does, which is the state a mode switched mid-batch leaves behind.
type gate struct {
	asks    map[string]bool
	refuses map[string]bool
	blind   bool

	// ask stands in for the question, and runs on whichever goroutine the
	// approval is happening on — which is the thing most of these tests are about.
	ask func(name string)

	mu   sync.Mutex
	seen []string
}

func (g *gate) Prompts(call llm.ToolCall) bool { return !g.blind && g.asks[call.Name] }

func (g *gate) Approve(ctx context.Context, call llm.ToolCall) error {
	g.mu.Lock()
	g.seen = append(g.seen, call.Name)
	g.mu.Unlock()

	if g.asks[call.Name] && g.ask != nil {
		g.ask(call.Name)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if g.refuses[call.Name] {
		return fmt.Errorf("permission denied: the user rejected %s", call.Name)
	}
	return nil
}

// asked is every call the gate was consulted about, in the order it saw them.
func (g *gate) asked() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.seen)
}

// timeline is what happened, in the order it happened, across the goroutines a
// batch runs on.
type timeline struct {
	mu    sync.Mutex
	notes []string
}

func (l *timeline) note(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.notes = append(l.notes, s)
}

func (l *timeline) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.notes)
}

// happenedBefore asserts one note precedes another, and fails loudly for a note
// that never happened at all rather than passing on the -1 that Index returns.
func happenedBefore(t *testing.T, notes []string, first, second string) {
	t.Helper()

	i, j := slices.Index(notes, first), slices.Index(notes, second)
	switch {
	case i < 0:
		t.Fatalf("%q never happened, so nothing here is being compared: %v", first, notes)
	case j < 0:
		t.Fatalf("%q never happened, so nothing here is being compared: %v", second, notes)
	case i > j:
		t.Errorf("%q happened after %q: %v", first, second, notes)
	}
}

// noted is a tool that says when it started and stopped, and attends a meeting
// in between so the test knows whether it overlapped its siblings.
func noted(l *timeline, m *meeting, name string) *stub {
	return &stub{name: name, run: func(context.Context, json.RawMessage) (tool.Result, error) {
		l.note(name + " started")
		m.attend(name)
		l.note(name + " finished")
		return tool.Result{Content: name + " ran"}, nil
	}}
}

func echoStub(name string) *stub {
	return &stub{name: name, run: func(context.Context, json.RawMessage) (tool.Result, error) {
		return tool.Result{Content: name + " ran"}, nil
	}}
}

func ranTimes(ran bool) int {
	if ran {
		return 1
	}
	return 0
}

// TestAPromptSplitsTheBatchAtTheCallItIsAbout is design §6 rule 5: everything
// ahead of the call runs concurrently and finishes, then the question is put,
// then that call runs on its own, then the rest of the batch carries on.
//
// The notes either side of the approved call are what say it ran alone —
// everything before it had finished and nothing after it had started — and the
// two meetings are what stop that being satisfied by running the whole batch one
// call at a time: each pair has to overlap, and a dispatcher that serialised
// everything to avoid the problem leaves both unmet.
func TestAPromptSplitsTheBatchAtTheCallItIsAbout(t *testing.T) {
	names := []string{"before1", "before2", "gated", "after1", "after2"}

	var log timeline
	first, second := newMeeting(2, neverWaited), newMeeting(2, neverWaited)

	g := &gate{
		asks: map[string]bool{"gated": true},
		ask:  func(name string) { log.note(name + " asked") },
	}
	// The approved call's own meeting is one wide, so it never waits: what it is
	// here for is the notes around it.
	tools := []tool.Tool{
		noted(&log, first, "before1"),
		noted(&log, first, "before2"),
		noted(&log, newMeeting(1, neverWaited), "gated"),
		noted(&log, second, "after1"),
		noted(&log, second, "after2"),
	}

	a := newAgent(t, agent.Config{
		Provider: fake.New(batch(names...)...),
		Tools:    registry(tools...),
		Approver: g,
	})
	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	notes := log.all()
	for _, name := range []string{"before1", "before2"} {
		happenedBefore(t, notes, name+" finished", "gated asked")
	}
	happenedBefore(t, notes, "gated asked", "gated started")
	for _, name := range []string{"after1", "after2"} {
		happenedBefore(t, notes, "gated finished", name+" started")
	}

	for _, m := range []struct {
		name string
		*meeting
	}{{"before the prompt", first}, {"after it", second}} {
		if peak, waited, _ := m.peaked(); peak != 2 || waited != 0 {
			t.Errorf("the pair %s peaked at %d calls at once and %d waited out the barrier alone; "+
				"a partition runs its calls together, and one call per group is not a split but a "+
				"serial batch", m.name, peak, waited)
		}
	}

	if got := g.asked(); !slices.Equal(got, names) {
		t.Errorf("the gate was consulted about %v; every call goes through it, in the order the "+
			"model asked", got)
	}

	want := make([]string, len(names))
	for i, name := range names {
		want[i] = name + " ran"
	}
	if got := contents(answers(t, a.Messages())); !slices.Equal(got, want) {
		t.Errorf("the batch answered %v; a partitioned batch still answers every call at its own "+
			"index, %v", got, want)
	}
}

// TestTheUserIsNeverAskedTwoThingsAtOnce is the property the split exists for:
// two prompts racing for one terminal is incoherent. The barrier is two wide, so
// meeting it at all is exactly the failure and every prompt waiting it out alone
// is the guarantee holding.
//
// The blind case is the same batch with the dispatcher's hint switched off,
// which is what a mode change between the hint and the approval leaves behind.
// It costs the split — the calls no longer partition where the prompts are — and
// it must still never produce two prompts at once, because every approval runs
// on the one goroutine driving the batch whatever the hint said.
func TestTheUserIsNeverAskedTwoThingsAtOnce(t *testing.T) {
	names := []string{"one", "two", "three", "four"}

	for _, tc := range []struct {
		name  string
		blind bool
	}{
		{"the dispatcher knows which calls ask", false},
		{"the dispatcher is told none of them does", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompts := newMeeting(2, alwaysWaits)
			g := &gate{
				asks:  map[string]bool{},
				blind: tc.blind,
				ask:   func(name string) { prompts.attend(name) },
			}
			tools := make([]tool.Tool, len(names))
			for i, name := range names {
				g.asks[name] = true
				tools[i] = &stub{name: name}
			}

			a := newAgent(t, agent.Config{
				Provider: fake.New(batch(names...)...),
				Tools:    registry(tools...),
				Approver: g,
			})
			if err := a.Send(context.Background(), "go"); err != nil {
				t.Fatalf("Send: %v", err)
			}

			peak, waited, order := prompts.peaked()
			if peak != 1 {
				t.Errorf("%d prompts were open at once; the user answers one question at a time", peak)
			}
			if waited != len(names) {
				t.Errorf("%d of %d prompts waited out the barrier alone; one that met it had a second "+
					"prompt open beside it", waited, len(names))
			}
			if !slices.Equal(order, names) {
				t.Errorf("the user was asked in the order %v; the batch is walked in the order the "+
					"model asked, %v", order, names)
			}
		})
	}
}

// TestAGateThatAsksNothingLeavesTheBatchWhole is the cost side of the barrier:
// an approver is installed on every turn once one is composed in, so a batch it
// waves through has to run exactly as it did without one.
func TestAGateThatAsksNothingLeavesTheBatchWhole(t *testing.T) {
	const width = 3
	names := []string{"alpha", "beta", "gamma"}

	m := newMeeting(width, neverWaited)
	tools := make([]tool.Tool, width)
	for i, name := range names {
		tools[i] = m.attendee(name)
	}

	g := &gate{}
	a := newAgent(t, agent.Config{
		Provider: fake.New(batch(names...)...),
		Tools:    registry(tools...),
		Approver: g,
	})
	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if peak, waited, _ := m.peaked(); peak != width || waited != 0 {
		t.Errorf("%d of %d calls ran at once and %d waited out the barrier alone; a gate that asks "+
			"nothing partitions nothing", peak, width, waited)
	}
	if got := g.asked(); !slices.Equal(got, names) {
		t.Errorf("the gate was consulted about %v, want %v; a call that skipped it ran ungated", got, names)
	}
}

// TestARefusalThatAsksNobodyLeavesTheBatchWhole: only a question splits a batch,
// because only a question needs the terminal to itself. A mode that refuses
// asks nobody, and plan mode is exactly this batch — reads that are allowed with
// a write that is denied among them — so splitting there would cost the
// parallelism of every batch the mode refuses anything in.
func TestARefusalThatAsksNobodyLeavesTheBatchWhole(t *testing.T) {
	names := []string{"read1", "denied", "read2"}

	m := newMeeting(2, neverWaited)
	g := &gate{refuses: map[string]bool{"denied": true}}
	a := newAgent(t, agent.Config{
		Provider: fake.New(batch(names...)...),
		Tools:    registry(m.attendee("read1"), echoStub("denied"), m.attendee("read2")),
		Approver: g,
	})
	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if peak, waited, _ := m.peaked(); peak != 2 || waited != 0 {
		t.Errorf("the two allowed calls peaked at %d at once and %d waited out the barrier alone; "+
			"the refusal between them is not a boundary they have to run either side of", peak, waited)
	}
	if got := answers(t, a.Messages())[1]; !got.IsError {
		t.Errorf("the refused call was answered with %q and no error flag", got.Content)
	}
}

// TestANameTheSnapshotDoesNotHoldIsNeverPutToTheUser: the call is resolved
// before it is gated (design §4 step 7), so a tool the model invented is
// answered by the loop rather than turned into a question about nothing — and
// into a barrier the rest of the batch waits behind.
func TestANameTheSnapshotDoesNotHoldIsNeverPutToTheUser(t *testing.T) {
	names := []string{"real", "teleport"}

	// A gate that asks about everything, so a call reaching it at all shows up.
	g := &gate{asks: map[string]bool{"real": true, "teleport": true}}
	a := newAgent(t, agent.Config{
		Provider: fake.New(batch(names...)...),
		Tools:    registry(echoStub("real")),
		Approver: g,
	})
	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := g.asked(); !slices.Equal(got, []string{"real"}) {
		t.Errorf("the gate was consulted about %v; a name no tool answers to is not the user's "+
			"question to answer", got)
	}
	if got := answers(t, a.Messages())[1]; !got.IsError || !strings.Contains(got.Content, "teleport") {
		t.Errorf("the invented call was answered with %q (error: %t); it has to name the tool that "+
			"is not there", got.Content, got.IsError)
	}
}

// TestACallTheGateRefusesIsAnsweredWithoutRunning: a refusal is information the
// model can act on, not a failure of the turn, so it comes back as an error
// result naming the reason and the rest of the batch carries on (design §3.4).
func TestACallTheGateRefusesIsAnsweredWithoutRunning(t *testing.T) {
	// The refusal that asked nobody comes last, behind a call still waiting for a
	// partition to run in, which is the batch where the announcement below arrives
	// before the call the model asked for first.
	names := []string{"refused", "allowed", "denied"}

	stubs := make([]*stub, len(names))
	tools := make([]tool.Tool, len(names))
	for i, name := range names {
		stubs[i] = echoStub(name)
		tools[i] = stubs[i]
	}

	// "refused" is a prompt the user answered no to; "denied" is a mode refusing
	// without asking anyone, which is the rung that never reaches a prompt.
	g := &gate{
		asks:    map[string]bool{"refused": true},
		refuses: map[string]bool{"refused": true, "denied": true},
	}
	var rec recorder
	a := newAgent(t, agent.Config{
		Provider: fake.New(batch(names...)...),
		Tools:    registry(tools...),
		Approver: g,
		Events:   rec.add,
	})
	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v; a refused call is not a failed turn", err)
	}

	msgs := a.Messages()
	wantPaired(t, msgs, len(names))
	results := answers(t, msgs)
	for i, name := range names {
		refused := g.refuses[name]
		switch {
		case refused && (!results[i].IsError || !strings.Contains(results[i].Content, name)):
			t.Errorf("%s was refused and answered with %q (error: %t); the model is told what was "+
				"refused and why", name, results[i].Content, results[i].IsError)
		case !refused && results[i].Content != name+" ran":
			t.Errorf("%s was allowed and answered with %q", name, results[i].Content)
		}
		if got, want := len(stubs[i].calls()), ranTimes(!refused); got != want {
			t.Errorf("%s was refused: %t, and ran %d time(s), want %d", name, refused, got, want)
		}
	}

	// A refusal nothing draws reads as the tool having quietly done nothing, so it
	// is announced as the call it was (design §7.8). Sorted, not compared in
	// place: events arrive in the order things happened, and a batch's calls
	// happen at once.
	want := slices.Sorted(slices.Values(names))
	for _, kind := range []agent.EventKind{agent.EventToolStart, agent.EventToolEnd} {
		var emitted []string
		for _, ev := range rec.of(kind) {
			emitted = append(emitted, ev.Tool)
		}
		slices.Sort(emitted)
		if !slices.Equal(emitted, want) {
			t.Errorf("%s was emitted for %v, want %v; a refused call is still a card in the UI",
				kind, emitted, want)
		}
	}
}

// TestATurnCancelledAtAPromptAnswersEveryCall: the interrupt lands while the
// user is being asked, which is the one moment a turn can sit indefinitely. What
// already ran keeps its result, the call being asked about and everything behind
// it are answered as interrupted, and the transcript stays one a provider will
// accept (design §4 invariant 1).
func TestATurnCancelledAtAPromptAnswersEveryCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	names := []string{"first", "gated", "last"}
	stubs := make([]*stub, len(names))
	tools := make([]tool.Tool, len(names))
	for i, name := range names {
		stubs[i] = echoStub(name)
		tools[i] = stubs[i]
	}

	g := &gate{asks: map[string]bool{"gated": true}, ask: func(string) { cancel() }}
	p := fake.New(batch(names...)...)
	a := newAgent(t, agent.Config{Provider: p, Tools: registry(tools...), Approver: g})

	if err := a.Send(ctx, "go"); !errors.Is(err, agent.ErrInterrupted) {
		t.Fatalf("Send = %v; the turn was cancelled while the user was being asked", err)
	}
	if got := len(p.Requests()); got != 1 {
		t.Errorf("the provider was called %d time(s); a cancelled turn takes no further step", got)
	}

	msgs := a.Messages()
	wantPaired(t, msgs, len(names))
	for i, block := range answers(t, msgs) {
		ran := i == 0
		switch {
		case ran && block.Content != names[i]+" ran":
			t.Errorf("%s ran before the interrupt and was answered with %q", names[i], block.Content)
		case !ran && !strings.Contains(block.Content, "interrupted"):
			t.Errorf("%s never ran and was answered with %q; a call the user stopped says so",
				names[i], block.Content)
		}
		if got, want := len(stubs[i].calls()), ranTimes(ran); got != want {
			t.Errorf("%s ran %d time(s), want %d", names[i], got, want)
		}
	}
}
