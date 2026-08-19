//go:build !windows

// The demo turn ends by running a real shell command, so this file is Unix-only
// for the same reason the bash tool's own tests are: design §14 cross-compiles
// the Windows targets from a linux runner and runs no tests on them.

package rasp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/fake"
	"github.com/theocod3s/rasp/internal/prompt"
	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tool/builtin"
	"github.com/theocod3s/rasp/internal/workspace"
)

// The file the demo works on, and the edit it makes to it.
const (
	demoFile = "config.go"

	demoBefore = `package demo

type Config struct {
	Model     string
	MaxTokens int
}
`

	// Anchored on the closing brace and the field above it, because the brace
	// alone appears twice and the edit tool refuses an ambiguous match rather
	// than picking one.
	demoAnchor = "	MaxTokens int\n}"
	demoMethod = "\n\nfunc (c Config) String() string { return c.Model }"

	// The command the model runs to check its own work. It names the file
	// relatively and on purpose: that is the assertion that a shell call and a
	// file tool agree on where the workspace is.
	demoCheck = "grep -n 'func (c Config) String() string' " + demoFile

	demoPrompt = "add a String() method to the Config struct and check it landed"
	demoReport = "Config now has a String() method returning its Model, and grep finds it in " + demoFile + "."

	demoModel = "fake-model"
)

// TestTheAgentReadsEditsRunsACommandAndReports is prd §9's demo, headless: the
// real loop, the real eight tools and a real temporary workspace, driven by the
// scripted provider so the sequence is fixed and CI needs no API key. The model
// is the only fake in it — every file the turn touches is a real one on disk,
// which is what makes this worth running above the package tests that already
// pass.
func TestTheAgentReadsEditsRunsACommandAndReports(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, demoFile), []byte(demoBefore), 0o644); err != nil {
		t.Fatalf("seeding the workspace: %v", err)
	}

	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatalf("opening the workspace at %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := ws.Close(); err != nil {
			t.Errorf("closing the workspace: %v", err)
		}
	})

	tools := tool.NewRegistry([]tool.Tool{
		builtin.NewBash(ws.Root()),
		builtin.Edit(ws),
		builtin.NewFind(ws),
		builtin.NewGrep(ws, builtin.RipgrepPath()),
		builtin.NewLs(ws),
		builtin.NewRead(ws, workspace.NewTracker()),
		builtin.NewTodos(),
		builtin.NewWrite(ws),
	})

	provider := fake.New(
		fake.Text("Reading ", demoFile, " first."),
		fake.ToolCall("read", args(t, map[string]any{"path": demoFile})),
		fake.Done(llm.StopToolUse),

		fake.ToolCall("edit", args(t, map[string]any{
			"path":       demoFile,
			"old_string": demoAnchor,
			"new_string": demoAnchor + demoMethod,
		})),
		fake.Done(llm.StopToolUse),

		fake.ToolCall("bash", args(t, map[string]any{"command": demoCheck})),
		fake.Done(llm.StopToolUse),

		fake.Text(demoReport),
		fake.Done(llm.StopEndTurn),
	)

	system := prompt.Build(prompt.Input{
		Model:        demoModel,
		Instructions: []string{"# Demo\n\nKeep the change to the struct it names."},
		Env: prompt.Env{
			Cwd:      ws.Root(),
			Platform: runtime.GOOS,
			Now:      time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC),
		},
	})

	var rec recorder
	a, err := agent.New(agent.Config{
		Provider:  provider,
		Tools:     tools,
		System:    system,
		Model:     demoModel,
		MaxTokens: 4096,
		Events:    rec.add,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}

	if err := a.Send(context.Background(), demoPrompt); err != nil {
		t.Fatalf("the demo turn failed: %v", err)
	}

	// Read back with os, not through the workspace: an assertion that goes down
	// the same path the tool wrote through would agree with a confinement bug
	// instead of catching one.
	after, err := os.ReadFile(filepath.Join(dir, demoFile))
	if err != nil {
		t.Fatalf("reading back the file the demo edited: %v", err)
	}
	if want := strings.Replace(demoBefore, demoAnchor, demoAnchor+demoMethod, 1); string(after) != want {
		t.Errorf("%s holds:\n%s\nwant:\n%s", demoFile, after, want)
	}

	ran := rec.of(agent.EventToolEnd)
	if names := toolsRun(ran); !slices.Equal(names, []string{"read", "edit", "bash"}) {
		t.Fatalf("the turn ran %v; the demo reads the file, edits it, then checks the edit", names)
	}
	for _, ev := range ran {
		if ev.Result.IsError {
			t.Errorf("%s failed: %s", ev.Tool, ev.Result.Content)
		}
	}

	// The read tool's own output, line-number prefixes and all, rather than
	// anything the fake produced: this is where a registry wired to a stub
	// instead of the real tool would show.
	if content := ran[0].Result.Content; !strings.Contains(content, "type Config struct {") {
		t.Errorf("read returned %q, which is not the file the demo seeded", content)
	}

	// grep printed a line number, which it can only have done by finding the file
	// the edit changed. Run anywhere but the workspace root, the command exits
	// non-zero with nothing to match.
	if content := ran[2].Result.Content; !strings.Contains(content, ":func (c Config) String() string") {
		t.Errorf("the check returned %q; %q run in %s prints the matching line and its number",
			content, demoCheck, ws.Root())
	}

	msgs := a.Messages()
	wantPairedTranscript(t, msgs, len(ran))

	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleAssistant || len(last.Content) != 1 || last.Content[0].Text != demoReport {
		t.Errorf("the turn ends with %+v; the demo ends by reporting what it did", last)
	}

	// What the loop put on the wire, which nothing below the provider can see.
	requests := provider.Requests()
	if len(requests) != 4 {
		t.Fatalf("the provider was called %d time(s); the demo is read, edit, check, report", len(requests))
	}
	wantTools := []string{"bash", "edit", "find", "grep", "ls", "read", "todos", "write"}
	for i, req := range requests {
		if names := specNames(req.Tools); !slices.Equal(names, wantTools) {
			t.Errorf("request %d offers %v, want %v in that order", i, names, wantTools)
		}
		if !slices.Equal(req.System, system) {
			t.Errorf("request %d carries system blocks %v, want %v", i, req.System, system)
		}
	}
}

// wantPairedTranscript asserts the transcript is one a provider will still
// accept: roles alternate from the prompt onward, and every message's tool_use
// ids are exactly the tool_result ids of the message after it, in order. An
// orphan either way is a 400 on every later request built from the transcript,
// so the session is bricked rather than degraded (design §4 invariant 1).
//
// calls is how many tool_use blocks the turn made, because a transcript holding
// none satisfies every rule above — which is the quietest way for this check to
// stop checking anything.
func wantPairedTranscript(t *testing.T, msgs []llm.Message, calls int) {
	t.Helper()

	if len(msgs) == 0 {
		t.Fatal("the transcript is empty, so there is nothing here to be valid or invalid")
	}

	uses := 0
	want := llm.RoleUser
	for i, msg := range msgs {
		if msg.Role != want {
			t.Fatalf("message %d of %d is a %q message where the transcript wants %q; the loop "+
				"alternates from the prompt onward", i, len(msgs), msg.Role, want)
		}
		want = llm.RoleAssistant
		if msg.Role == llm.RoleAssistant {
			want = llm.RoleUser
		}

		asked := blockIDs(msg, llm.BlockToolUse)
		uses += len(asked)

		var answered []string
		if i+1 < len(msgs) {
			answered = blockIDs(msgs[i+1], llm.BlockToolResult)
		}
		if !slices.Equal(asked, answered) {
			t.Fatalf("message %d asks for tool_use ids %v and the message after it answers %v; every "+
				"provider rejects a transcript where those two differ", i, asked, answered)
		}
	}

	if uses != calls {
		t.Fatalf("the transcript holds %d tool_use block(s) and the turn made %d call(s)", uses, calls)
	}
}

func blockIDs(msg llm.Message, kind llm.BlockType) []string {
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

// recorder collects the turn's events. No lock: the agent serialises the
// callback (decisions.md), and the test reads them once Send has returned.
type recorder struct{ events []agent.Event }

func (r *recorder) add(ev agent.Event) { r.events = append(r.events, ev) }

func (r *recorder) of(kind agent.EventKind) []agent.Event {
	var out []agent.Event
	for _, ev := range r.events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func toolsRun(events []agent.Event) []string {
	out := make([]string, len(events))
	for i, ev := range events {
		out[i] = ev.Tool
	}
	return out
}

func specNames(specs []llm.ToolSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}

// args is one tool call's arguments as the model would have sent them.
func args(t *testing.T, in map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("encoding %v as tool arguments: %v", in, err)
	}
	return string(raw)
}
