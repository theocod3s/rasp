package agent_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"go.uber.org/goleak"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tool"
)

// TestMain runs the leak detector over the package. The loop dispatches
// sequentially today and spawns nothing; the check is in place before the batch
// starts running concurrently, because a goroutine outliving a turn is a hung
// process on quit (design §13).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// newAgent fills in whatever a test did not care about.
func newAgent(t *testing.T, cfg agent.Config) *agent.Agent {
	t.Helper()
	if cfg.Model == "" {
		cfg.Model = "test-model"
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 1024
	}
	if cfg.Tools == nil {
		cfg.Tools = tool.NewRegistry(nil)
	}
	a, err := agent.New(cfg)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return a
}

// stub is a Tool assembled per test. Hand-rolled rather than built with
// tool.New, because these tests assert on the exact bytes the model sent and a
// reflected tool unmarshals them before its handler sees anything.
type stub struct {
	name  string
	calls []json.RawMessage
	run   func(context.Context, json.RawMessage) (tool.Result, error)
}

func (s *stub) Name() string           { return s.name }
func (s *stub) Description() string    { return s.name + " is a tool for a test" }
func (s *stub) Schema() map[string]any { return map[string]any{"type": "object"} }

func (s *stub) Run(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	s.calls = append(s.calls, raw)
	if s.run == nil {
		return tool.Result{Content: "ok"}, nil
	}
	return s.run(ctx, raw)
}

func registry(tools ...tool.Tool) *tool.Registry { return tool.NewRegistry(tools) }

// recorder collects the turn's events. They arrive on the goroutine running the
// turn, so a test reads them once Send has returned.
type recorder struct{ events []agent.Event }

func (r *recorder) add(ev agent.Event) { r.events = append(r.events, ev) }

func (r *recorder) kinds() []agent.EventKind {
	out := make([]agent.EventKind, len(r.events))
	for i, ev := range r.events {
		out[i] = ev.Kind
	}
	return out
}

func (r *recorder) of(kind agent.EventKind) []agent.Event {
	var out []agent.Event
	for _, ev := range r.events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// wantPaired is wantValid over a transcript whose shape the test knows: calls is
// how many tool_use blocks it should hold, because a transcript with none passes
// every assertion in wantValid — which is the quietest way for this check to stop
// checking anything.
func wantPaired(t *testing.T, msgs []llm.Message, calls int) {
	t.Helper()

	var uses []string
	for _, msg := range msgs {
		for _, block := range msg.Content {
			if block.Type == llm.BlockToolUse {
				uses = append(uses, block.ID)
			}
		}
	}
	if len(uses) != calls {
		t.Fatalf("the transcript holds %d tool_use block(s) and the test expects %d: %v", len(uses), calls, uses)
	}
	wantValid(t, msgs)
}

// wantValid asserts the transcript is one a provider will still accept, whatever
// the turn did or how it ended.
//
// Two properties, checked in one comparison per message. Roles alternate from the
// user's prompt onward, so two replies in a row — a step that committed the
// assistant message without its results, or committed it twice — is caught. And
// every message's tool_use ids are exactly the tool_result ids of the message
// after it, in order: a call answered late, out of order, in a message of its
// own, or not at all all read as a mismatch, as does a result answering nothing.
// The last message is compared against no successor, which is what makes a
// dangling call at the end of an interrupted turn a failure here.
//
// An orphan either way is a 400 on this request and on every request built from
// the transcript afterwards, so the session is bricked rather than degraded
// (design §4 invariant 1).
func wantValid(t *testing.T, msgs []llm.Message) {
	t.Helper()

	ids := func(msg llm.Message, kind llm.BlockType) []string {
		var out []string
		for _, block := range msg.Content {
			switch {
			case block.Type != kind:
			case kind == llm.BlockToolUse:
				out = append(out, block.ID)
			default:
				out = append(out, block.ToolUseID)
			}
		}
		return out
	}

	want := llm.RoleUser
	for i, msg := range msgs {
		if msg.Role != want {
			t.Fatalf("message %d of %d is a %q message where the transcript wants %q; the loop "+
				"alternates from the prompt onward", i, len(msgs), msg.Role, want)
		}
		if want == llm.RoleUser {
			want = llm.RoleAssistant
		} else {
			want = llm.RoleUser
		}

		var answers []string
		if i+1 < len(msgs) {
			answers = ids(msgs[i+1], llm.BlockToolResult)
		}
		if uses := ids(msg, llm.BlockToolUse); !slices.Equal(uses, answers) {
			t.Fatalf("message %d asks for tool_use ids %v and the message after it answers %v; every "+
				"provider rejects a transcript where those two differ", i, uses, answers)
		}
	}
}

// blocks names the block types of one message, for a failure that says what the
// message actually held.
func blocks(msg llm.Message) []llm.BlockType {
	out := make([]llm.BlockType, len(msg.Content))
	for i, b := range msg.Content {
		out[i] = b.Type
	}
	return out
}

func toolNames(specs []llm.ToolSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}
