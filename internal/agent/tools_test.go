package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/fake"
	"github.com/theocod3s/rasp/internal/tool"
)

// answer pulls the tool_result blocks out of a finished transcript, in the order
// a provider will read them.
func answers(t *testing.T, msgs []llm.Message) []llm.Block {
	t.Helper()
	var out []llm.Block
	for _, msg := range msgs {
		for _, block := range msg.Content {
			if block.Type == llm.BlockToolResult {
				out = append(out, block)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("the transcript holds no tool_result block, so there is nothing here to examine")
	}
	return out
}

// oneCall scripts a turn that calls name and a second that answers, which is the
// shape every dispatch case below shares.
func oneCall(name string) *fake.Provider {
	return fake.New(
		fake.ToolCall(name, `{"n":1}`),
		fake.Done(llm.StopToolUse),
		fake.Text("noted"),
		fake.Done(llm.StopEndTurn),
	)
}

// TestACallToATheModelInventedComesBackAsAnError: a name that is not in the
// snapshot is a conversation rather than a failure — the model reads it and
// picks a tool that exists (design §12).
func TestACallToATheModelInventedComesBackAsAnError(t *testing.T) {
	p := oneCall("teleport")
	a := newAgent(t, agent.Config{Provider: p, Tools: registry(&stub{name: "read"})})

	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v; an unknown tool does not end the turn", err)
	}
	result := answers(t, a.Messages())[0]
	if !result.IsError || !strings.Contains(result.Content, "teleport") {
		t.Errorf("the call was answered with %q (error: %t); it has to name the tool that is not there",
			result.Content, result.IsError)
	}
	if len(p.Requests()) != 2 {
		t.Errorf("the provider was called %d time(s); the model gets to try again", len(p.Requests()))
	}
}

// TestAToolThatCouldNotRunAtAllStillAnswersTheModel: the Go error return means
// the tool could not run, and the loop converts it rather than propagating it
// (design §3.4).
func TestAToolThatCouldNotRunAtAllStillAnswersTheModel(t *testing.T) {
	broken := &stub{name: "broken", run: func(context.Context, json.RawMessage) (tool.Result, error) {
		return tool.Result{}, errors.New("the socket is not there")
	}}

	p := oneCall("broken")
	a := newAgent(t, agent.Config{Provider: p, Tools: registry(broken)})

	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v; a tool that could not run does not end the turn", err)
	}
	result := answers(t, a.Messages())[0]
	if !result.IsError || !strings.Contains(result.Content, "the socket is not there") {
		t.Errorf("the call was answered with %q (error: %t); the model needs what went wrong",
			result.Content, result.IsError)
	}
}

// TestAToolThatFailedIsNotATurnThatFailed: IsError with a nil Go error is
// information, and the turn carries on with it.
func TestAToolThatFailedIsNotATurnThatFailed(t *testing.T) {
	failing := &stub{name: "test", run: func(context.Context, json.RawMessage) (tool.Result, error) {
		return tool.Result{IsError: true, Content: "2 tests failed"}, nil
	}}

	p := oneCall("test")
	a := newAgent(t, agent.Config{Provider: p, Tools: registry(failing)})

	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v; a failing tool is conversation, not an exception", err)
	}
	if result := answers(t, a.Messages())[0]; !result.IsError || result.Content != "2 tests failed" {
		t.Errorf("the call was answered with %q (error: %t)", result.Content, result.IsError)
	}
}

// TestAPanickingToolIsAFailedCallAndNotADeadProcess asserts the loop dispatches
// through tool.RunSafely; what the guard itself does is that function's own test.
func TestAPanickingToolIsAFailedCallAndNotADeadProcess(t *testing.T) {
	// The guard logs the stack, and slog's default writes it to stderr under a
	// test; logx points that at a file in a real run (design §2).
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	exploding := &stub{name: "boom", run: func(context.Context, json.RawMessage) (tool.Result, error) {
		panic("index out of range")
	}}

	p := oneCall("boom")
	a := newAgent(t, agent.Config{Provider: p, Tools: registry(exploding)})

	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v; a panicking tool does not end the turn", err)
	}
	result := answers(t, a.Messages())[0]
	if !result.IsError || !strings.Contains(result.Content, "index out of range") {
		t.Errorf("the call was answered with %q (error: %t)", result.Content, result.IsError)
	}
}

// A meeting nothing should ever wait out, and one everything should: the first
// is long enough that a scheduler under load still reaches the barrier, and the
// second short enough that a serial batch pays it once per call.
const (
	neverWaited = 2 * time.Second
	alwaysWaits = 100 * time.Millisecond
)

// batch scripts one reply asking for every named tool, each with its own
// arguments — distinct because a batch of six identical calls is a loop the turn
// halts on (design §4 invariant 3), and none of these tests is about that.
func batch(names ...string) []fake.Step {
	steps := make([]fake.Step, 0, len(names)+3)
	for i, name := range names {
		steps = append(steps, fake.ToolCall(name, fmt.Sprintf(`{"n":%d}`, i)))
	}
	return append(steps, fake.Done(llm.StopToolUse), fake.Text("all done"), fake.Done(llm.StopEndTurn))
}

func contents(blocks []llm.Block) []string {
	out := make([]string, len(blocks))
	for i, block := range blocks {
		out[i] = block.Content
	}
	return out
}

// TestABatchRunsItsToolsAtTheSameTime is design §6 rule 4: parallel is the
// default and the model is never asked. Three calls meet a barrier none of them
// can reach alone, so the overlap is what the batch did rather than what a clock
// suggests it did.
func TestABatchRunsItsToolsAtTheSameTime(t *testing.T) {
	const width = 3
	names := []string{"alpha", "beta", "gamma"}

	m := newMeeting(width, neverWaited)
	tools := make([]tool.Tool, width)
	for i, name := range names {
		tools[i] = m.attendee(name)
	}

	a := newAgent(t, agent.Config{Provider: fake.New(batch(names...)...), Tools: registry(tools...)})
	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	peak, alone, _ := m.peaked()
	if peak != width {
		t.Errorf("at most %d of %d calls were ever running at once; tools run concurrently by default",
			peak, width)
	}
	if alone != 0 {
		t.Errorf("%d call(s) waited %s at the barrier with no sibling arriving, so the batch was not "+
			"running them together", alone, neverWaited)
	}

	got := contents(answers(t, a.Messages()))
	want := []string{"alpha ran", "beta ran", "gamma ran"}
	if !slices.Equal(got, want) {
		t.Errorf("the results read %v; the model asked in the order %v, and completion order is not "+
			"result order", got, want)
	}
}

// TestOneSequentialCallMakesTheWholeBatchSerial is the conservative half of the
// same rule: a tool declares itself sequential because it touches state it does
// not own, so running it beside unaudited siblings is the thing it is declaring
// against. Every call waits out the barrier here, which is only possible if no
// two were ever inside it together.
func TestOneSequentialCallMakesTheWholeBatchSerial(t *testing.T) {
	const width = 3
	names := []string{"alpha", "beta", "gamma"}

	m := newMeeting(width, alwaysWaits)
	tools := make([]tool.Tool, width)
	for i, name := range names {
		attendee := m.attendee(name)
		// The declaring tool is in the middle, so a dispatcher that only consulted
		// the first call would run this batch concurrently.
		if i == 1 {
			tools[i] = &serialStub{attendee}
			continue
		}
		tools[i] = attendee
	}

	a := newAgent(t, agent.Config{Provider: fake.New(batch(names...)...), Tools: registry(tools...)})
	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	peak, alone, order := m.peaked()
	if peak != 1 {
		t.Errorf("%d calls were running at once; one sequential call makes the whole batch serial", peak)
	}
	if alone != width {
		t.Errorf("%d of %d calls waited out the barrier alone; a serial batch can never meet it, so "+
			"anything less means two of them overlapped", alone, width)
	}
	if !slices.Equal(order, names) {
		t.Errorf("the calls ran in the order %v; a serial batch runs them as the model asked, %v", order, names)
	}

	got := contents(answers(t, a.Messages()))
	want := []string{"alpha ran", "beta ran", "gamma ran"}
	if !slices.Equal(got, want) {
		t.Errorf("the results read %v; the model asked in the order %v", got, want)
	}
}

// TestABatchRunsNoMoreThanTheCapAtOnce holds the third of internals §6.2's four
// mechanisms: unbounded goroutines against a slow filesystem is its own failure
// mode. The barrier is one wider than the cap, so meeting it is exactly the
// failure — and every call waiting it out is the cap holding.
func TestABatchRunsNoMoreThanTheCapAtOnce(t *testing.T) {
	// The batch and the barrier below are both sized off the constant, so they
	// prove the cap is enforced and say nothing about what it is. Design §6 rule 4
	// and internals §6.2 both fix the number, and without this line raising it
	// would raise the test with it and nothing would notice.
	if agent.MaxParallelTools != 8 {
		t.Errorf("the cap is %d; the number is 8", agent.MaxParallelTools)
	}

	calls := agent.MaxParallelTools + 4

	m := newMeeting(agent.MaxParallelTools+1, alwaysWaits)
	names := make([]string, calls)
	tools := make([]tool.Tool, calls)
	for i := range calls {
		names[i] = fmt.Sprintf("wait%d", i)
		tools[i] = m.attendee(names[i])
	}

	a := newAgent(t, agent.Config{Provider: fake.New(batch(names...)...), Tools: registry(tools...)})
	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	peak, alone, _ := m.peaked()
	if peak != agent.MaxParallelTools {
		t.Errorf("%d of %d calls ran at once under a cap of %d", peak, calls, agent.MaxParallelTools)
	}
	if alone != calls {
		t.Errorf("only %d of %d calls waited out the barrier alone; the barrier is one wider than the "+
			"cap, so meeting it at all means the cap let an extra call through", alone, calls)
	}
	if got := len(answers(t, a.Messages())); got != calls {
		t.Errorf("the batch answered %d of its %d calls", got, calls)
	}
}

// TestAMixedBatchAnswersEveryCallInOrder is the batch as it actually arrives: a
// tool that works, one that fails, one that cannot run, one that panics, and a
// name that is not on the list, all at once. Under -race it is also the assertion
// that dispatching them together shares nothing.
func TestAMixedBatchAnswersEveryCallInOrder(t *testing.T) {
	// The panic guard logs the stack, and slog's default writes it to stderr under
	// a test.
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	m := newMeeting(3, neverWaited)
	tools := []tool.Tool{
		m.attendee("slow"),
		&stub{name: "quick", run: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{Content: "quick ran"}, nil
		}},
		&stub{name: "failing", run: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{IsError: true, Content: "2 tests failed"}, nil
		}},
		&stub{name: "broken", run: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{}, errors.New("the socket is not there")
		}},
		&stub{name: "exploding", run: func(context.Context, json.RawMessage) (tool.Result, error) {
			m.attend("exploding")
			panic("index out of range")
		}},
		m.attendee("also-slow"),
	}

	names := []string{"slow", "quick", "failing", "broken", "exploding", "teleport", "also-slow"}
	var rec recorder
	a := newAgent(t, agent.Config{Provider: fake.New(batch(names...)...), Tools: registry(tools...), Events: rec.add})

	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v; none of these calls ends the turn", err)
	}

	if peak, alone, _ := m.peaked(); peak != 3 || alone != 0 {
		t.Errorf("%d of the batch's calls overlapped and %d waited alone; the three that meet the "+
			"barrier include the one that panics, so the guard has to run beside its siblings", peak, alone)
	}

	msgs := a.Messages()
	wantPaired(t, msgs, len(names))
	results := answers(t, msgs)

	want := []struct {
		content string
		isError bool
	}{
		{"slow ran", false},
		{"quick ran", false},
		{"2 tests failed", true},
		{"the socket is not there", true},
		{"index out of range", true},
		{"teleport", true},
		{"also-slow ran", false},
	}
	for i, w := range want {
		if !strings.Contains(results[i].Content, w.content) || results[i].IsError != w.isError {
			t.Errorf("call %d (%s) was answered with %q (error: %t); it belongs at index %d holding %q",
				i, names[i], results[i].Content, results[i].IsError, i, w.content)
		}
	}

	// One start and one end per call, whatever order they finished in: a frontend
	// keys its spinner off the pair.
	started, ended := map[string]int{}, map[string]int{}
	for _, ev := range rec.of(agent.EventToolStart) {
		started[ev.CallID]++
	}
	for _, ev := range rec.of(agent.EventToolEnd) {
		ended[ev.CallID]++
	}
	if len(started) != len(names) || len(ended) != len(names) {
		t.Fatalf("the batch emitted starts for %d call(s) and ends for %d, over %d calls",
			len(started), len(ended), len(names))
	}
	for id, n := range started {
		if n != 1 || ended[id] != 1 {
			t.Errorf("call %s was announced %d time(s) and finished %d", id, n, ended[id])
		}
	}
}

// TestResultsComeBackInTheOrderTheModelAskedFor is the ordering rule at its
// simplest: every provider rejects a request whose tool_result order differs from
// its tool_use order (design §6 rule 6).
func TestResultsComeBackInTheOrderTheModelAskedFor(t *testing.T) {
	each := func(name string) *stub {
		return &stub{name: name, run: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{Content: name + " ran"}, nil
		}}
	}

	p := fake.New(
		fake.ToolCall("alpha"),
		fake.ToolCall("beta"),
		fake.ToolCall("gamma"),
		fake.Done(llm.StopToolUse),
		fake.Text("all three"),
		fake.Done(llm.StopEndTurn),
	)
	a := newAgent(t, agent.Config{Provider: p, Tools: registry(each("alpha"), each("beta"), each("gamma"))})

	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	msgs := a.Messages()
	wantPaired(t, msgs, 3)

	var contents []string
	for _, block := range answers(t, msgs) {
		contents = append(contents, block.Content)
	}
	want := []string{"alpha ran", "beta ran", "gamma ran"}
	if !slices.Equal(contents, want) {
		t.Errorf("the results read %v; the model asked in the order %v", contents, want)
	}
	if got := len(msgs[2].Content); got != 3 {
		t.Errorf("the results arrived as %d block(s) in one message; three calls are answered together", got)
	}
}
