package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

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

// TestResultsComeBackInTheOrderTheModelAskedFor is the ordering rule at its
// simplest, with dispatch still sequential: every provider rejects a request
// whose tool_result order differs from its tool_use order (design §6 rule 6).
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
