package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/fake"
	"github.com/theocod3s/rasp/internal/tool"
)

// TestTwoStepTurnCallsAToolAndThenAnswers is the loop end to end: the model asks
// for a tool, the result goes back as a user message, and the second call
// answers from it (internals §2).
func TestTwoStepTurnCallsAToolAndThenAnswers(t *testing.T) {
	read := &stub{name: "read", run: func(context.Context, json.RawMessage) (tool.Result, error) {
		return tool.Result{Content: "package main"}, nil
	}}

	p := fake.New(
		fake.Text("Let me look at that."),
		fake.ToolCall("read", `{"path":"main.go"}`),
		fake.Done(llm.StopToolUse),

		fake.Text("It prints hello."),
		fake.Done(llm.StopEndTurn),
	)

	var rec recorder
	a := newAgent(t, agent.Config{Provider: p, Tools: registry(read), Events: rec.add})
	if err := a.Send(context.Background(), "what does main.go do?"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	calls := read.calls()
	if len(calls) != 1 {
		t.Fatalf("read ran %d time(s); the script asks for it once", len(calls))
	}
	if got := string(calls[0]); got != `{"path":"main.go"}` {
		t.Errorf("read received %s; the model sent {\"path\":\"main.go\"}", got)
	}

	msgs := a.Messages()
	if len(msgs) != 4 {
		t.Fatalf("the turn left %d message(s); a two-step turn leaves the prompt, the first reply, "+
			"its results and the answer", len(msgs))
	}
	wantPaired(t, msgs, 1)

	if roles := []llm.Role{msgs[0].Role, msgs[1].Role, msgs[2].Role, msgs[3].Role}; !slices.Equal(roles,
		[]llm.Role{llm.RoleUser, llm.RoleAssistant, llm.RoleUser, llm.RoleAssistant}) {
		t.Errorf("roles are %v; tool results go back as a user message (internals §2.3)", roles)
	}
	if got := blocks(msgs[1]); !slices.Equal(got, []llm.BlockType{llm.BlockText, llm.BlockToolUse}) {
		t.Errorf("the first reply holds %v; the script streamed text and then a call", got)
	}
	if result := msgs[2].Content[0]; result.Content != "package main" || result.IsError {
		t.Errorf("the tool result is %q (error: %t); the tool returned %q", result.Content, result.IsError, "package main")
	}
	if got := msgs[3].Content[0].Text; got != "It prints hello." {
		t.Errorf("the answer is %q; the script's second turn says %q", got, "It prints hello.")
	}

	// The second call re-sends everything: the model has no memory between calls,
	// so the whole transcript goes out each time (internals §1).
	reqs := p.Requests()
	if len(reqs) != 2 {
		t.Fatalf("the provider was called %d time(s); a tool call and its answer are two", len(reqs))
	}
	if len(reqs[1].Messages) != 3 {
		t.Errorf("the second call carried %d message(s); the prompt, the reply and its results are three",
			len(reqs[1].Messages))
	}
	if names := toolNames(reqs[0].Tools); !slices.Equal(names, []string{"read"}) {
		t.Errorf("the request offered %v; the registry holds read", names)
	}

	want := []agent.EventKind{
		// text, then the call's block opening and its arguments arriving
		agent.EventAssistantDelta, agent.EventAssistantDelta, agent.EventAssistantDelta,
		agent.EventAssistantEnd,
		agent.EventToolStart, agent.EventToolEnd,
		agent.EventAssistantDelta,
		agent.EventAssistantEnd,
		agent.EventTurnEnd,
	}
	if got := rec.kinds(); !slices.Equal(got, want) {
		t.Errorf("the turn emitted\n\t%v\nand the script produces\n\t%v", got, want)
	}
}

func TestToolEventsCarryTheCallTheModelMade(t *testing.T) {
	p := fake.New(
		fake.ToolCall("read", `{"path":"main.go"}`),
		fake.Done(llm.StopToolUse),
		fake.Text("done"),
		fake.Done(llm.StopEndTurn),
	)

	var rec recorder
	a := newAgent(t, agent.Config{Provider: p, Tools: registry(&stub{name: "read"}), Events: rec.add})
	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	start := rec.of(agent.EventToolStart)
	end := rec.of(agent.EventToolEnd)
	if len(start) != 1 || len(end) != 1 {
		t.Fatalf("the turn emitted %d start and %d end event(s) for one call", len(start), len(end))
	}
	if start[0].Tool != "read" || start[0].CallID != "call_1" || string(start[0].Input) != `{"path":"main.go"}` {
		t.Errorf("the start event says tool %q, call %q, input %s", start[0].Tool, start[0].CallID, start[0].Input)
	}
	if end[0].Result == nil || end[0].Result.Content != "ok" {
		t.Errorf("the end event carries %+v; the stub answers \"ok\"", end[0].Result)
	}
}

// TestTheTurnHoldsOneToolSnapshot is design §3.3: a server appearing mid-turn
// must not change the list, because the tool list sits inside the cached prompt
// prefix and an unstable one destroys the cache on every request.
func TestTheTurnHoldsOneToolSnapshot(t *testing.T) {
	reg := registry()
	adder := &stub{name: "alpha", run: func(context.Context, json.RawMessage) (tool.Result, error) {
		reg.Replace("mcp:test", []tool.Tool{&stub{name: "beta"}})
		return tool.Result{Content: "registered"}, nil
	}}
	reg.Replace("under-test", []tool.Tool{adder})

	p := fake.New(
		fake.ToolCall("alpha"),
		fake.Done(llm.StopToolUse),
		fake.Text("done"),
		fake.Done(llm.StopEndTurn),
	)

	a := newAgent(t, agent.Config{Provider: p, Tools: reg})
	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	reqs := p.Requests()
	if len(reqs) != 2 {
		t.Fatalf("the provider was called %d time(s); the script holds two turns", len(reqs))
	}
	first, second := toolNames(reqs[0].Tools), toolNames(reqs[1].Tools)
	if !slices.Equal(first, second) {
		t.Errorf("the turn offered %v and then %v; a tool registered mid-turn belongs to the next "+
			"turn's snapshot", first, second)
	}
	if !slices.Contains(toolNames(reg.Snapshot().Specs()), "beta") {
		t.Fatal("the registry never took the new tool, so this test proved nothing about snapshots")
	}
}

func TestTurnEndCarriesWhatEveryStepSpent(t *testing.T) {
	p := fake.New(
		fake.Usage(llm.Usage{Input: 10, Output: 2, CacheRead: 5}),
		fake.ToolCall("noop"),
		fake.Done(llm.StopToolUse),

		fake.Usage(llm.Usage{Input: 20, Output: 3, CacheWrite: 7}),
		fake.Text("done"),
		fake.Done(llm.StopEndTurn),
	)

	var rec recorder
	a := newAgent(t, agent.Config{Provider: p, Tools: registry(&stub{name: "noop"}), Events: rec.add})
	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	ends := rec.of(agent.EventTurnEnd)
	if len(ends) != 1 {
		t.Fatalf("the turn emitted %d end event(s); every turn ends with exactly one", len(ends))
	}
	want := llm.Usage{Input: 30, Output: 5, CacheRead: 5, CacheWrite: 7}
	if ends[0].Usage != want {
		t.Errorf("the turn reports %+v; its two steps spent %+v", ends[0].Usage, want)
	}
}

func TestASecondSendIsRefusedWhileATurnIsRunning(t *testing.T) {
	var (
		a     *agent.Agent
		inner error
	)
	recur := &stub{name: "recur", run: func(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
		inner = a.Send(ctx, "and another thing")
		return tool.Result{Content: "ok"}, nil
	}}

	// Three turns for a two-turn script: the extra one is what a nested Send
	// would consume, so a missing guard fails on the assertion below rather than
	// on the fake running out of script.
	p := fake.New(
		fake.ToolCall("recur"),
		fake.Done(llm.StopToolUse),
		fake.Text("done"),
		fake.Done(llm.StopEndTurn),
		fake.Text("done again"),
		fake.Done(llm.StopEndTurn),
	)

	a = newAgent(t, agent.Config{Provider: p, Tools: registry(recur)})
	if err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !errors.Is(inner, agent.ErrTurnInProgress) {
		t.Errorf("the second Send returned %v; two turns on one transcript would interleave", inner)
	}
}

// TestAnEmptyPromptIsRefusedBeforeItReachesAProvider: an assistant message with
// nothing sendable in it is a state the transcript tolerates, and a user message
// with nothing in it is a bug, because rasp writes those (decisions.md).
func TestAnEmptyPromptIsRefusedBeforeItReachesAProvider(t *testing.T) {
	p := fake.New(fake.Text("here you go"), fake.Done(llm.StopEndTurn))
	a := newAgent(t, agent.Config{Provider: p})

	if err := a.Send(context.Background(), ""); err == nil {
		t.Fatal("Send accepted an empty prompt, which every provider then refuses")
	}
	if got := len(p.Requests()); got != 0 {
		t.Errorf("the provider was called %d time(s) with a message it would reject", got)
	}
	if msgs := a.Messages(); len(msgs) != 0 {
		t.Errorf("the transcript holds %d message(s); an empty prompt leaves none", len(msgs))
	}
}

func TestNewRefusesAConfigThatCannotRunATurn(t *testing.T) {
	full := func() agent.Config {
		return agent.Config{
			Provider:  fake.New(),
			Tools:     registry(),
			Model:     "test-model",
			MaxTokens: 1024,
		}
	}

	cases := []struct {
		name   string
		remove func(*agent.Config)
	}{
		{"no provider", func(c *agent.Config) { c.Provider = nil }},
		{"no registry", func(c *agent.Config) { c.Tools = nil }},
		{"no model", func(c *agent.Config) { c.Model = "" }},
		{"no reply cap", func(c *agent.Config) { c.MaxTokens = 0 }},
		{"negative fuse", func(c *agent.Config) { c.MaxSteps = -1 }},
	}

	if _, err := agent.New(full()); err != nil {
		t.Fatalf("the complete config was refused (%v), so every case below would pass for the wrong reason", err)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := full()
			c.remove(&cfg)
			if _, err := agent.New(cfg); err == nil {
				t.Error("accepted, and the turn would fail at its first model call instead")
			}
		})
	}
}
