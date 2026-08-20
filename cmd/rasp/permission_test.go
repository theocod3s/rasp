package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/config"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/fake"
	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/workspace"
)

// TestEveryToolIsAskedAboutAsWhatItDoes. A request carries an action, a resolved
// path and a command, and none of the three is in a tool call: this mapping is
// the only place that knows which is which (decisions.md). Getting it wrong is
// quiet — a grant keyed on the wrong fields covers nothing, or covers
// everything, and either way the ladder still answers.
func TestEveryToolIsAskedAboutAsWhatItDoes(t *testing.T) {
	dir := t.TempDir()
	g := gateFor(t, config.Config{Mode: config.ModeManual}, dir)
	// The workspace resolves its own root's symlinks, which on macOS is the
	// difference between /var and /private/var.
	root := canonical(t, dir)

	tests := []struct {
		name    string
		call    llm.ToolCall
		action  permission.Action
		path    string
		command string
	}{
		{
			name:   "read only looks",
			call:   call("read", `{"path":"auth.go"}`),
			action: permission.ActionRead,
			path:   filepath.Join(root, "auth.go"),
		},
		{name: "grep only looks", call: call("grep", `{"pattern":"parse"}`), action: permission.ActionRead},
		{name: "find only looks", call: call("find", `{"pattern":"*.go"}`), action: permission.ActionRead},
		{name: "ls only looks", call: call("ls", `{}`), action: permission.ActionRead},
		{
			// It writes a list in memory and touches neither the filesystem nor a
			// shell, so it belongs with the tools that only look.
			name:   "todos touches nothing outside the process",
			call:   call("todos", `{"todos":[]}`),
			action: permission.ActionRead,
		},
		{
			name:   "edit changes a file",
			call:   call("edit", `{"path":"auth.go","old_string":"a","new_string":"b"}`),
			action: permission.ActionEdit,
			path:   filepath.Join(root, "auth.go"),
		},
		{
			name:   "write creates one",
			call:   call("write", `{"path":"new.go","content":"package demo"}`),
			action: permission.ActionWrite,
			path:   filepath.Join(root, "new.go"),
		},
		{
			name:    "bash carries the command line",
			call:    call("bash", `{"command":"rm -rf dist"}`),
			action:  permission.ActionExecute,
			command: "rm -rf dist",
		},
		{
			// A server's own tool matched against the bash table would be judged
			// by patterns written for shell commands, where auto allows nearly
			// everything. The tool name is what an MCP rule matches (design §8.2).
			name:   "an mcp tool is never read as a shell command",
			call:   call("mcp__db__query", `{"command":"DROP TABLE users"}`),
			action: permission.ActionExecute,
		},
		{
			// The workspace refuses it and so will the tool. What matters here is
			// that the request still names it: keyed on the empty string, one
			// "always" would cover every path a later call could name.
			name:   "a path outside the workspace is carried as written",
			call:   call("read", `{"path":"../../etc/passwd"}`),
			action: permission.ActionRead,
			path:   "../../etc/passwd",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := g.request(tc.call)

			want := permission.Request{
				CallID:  tc.call.ID,
				Tool:    tc.call.Name,
				Action:  tc.action,
				Path:    tc.path,
				Command: tc.command,
			}
			if got != want {
				t.Errorf("the ladder is asked about %+v, want %+v", got, want)
			}
		})
	}
}

// TestOneFileIsOneGrantHoweverItIsSpelled. A grant is keyed on the path
// (design §7.7), so every spelling of one file has to resolve to one string —
// otherwise approving `./auth.go` leaves `auth.go` and the symlink to it asking
// again, and the "always" the user gave covers a file nothing will name twice.
func TestOneFileIsOneGrantHoweverItIsSpelled(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "auth.go")
	if err := os.WriteFile(file, []byte("package demo\n"), 0o644); err != nil {
		t.Fatalf("seeding the workspace: %v", err)
	}
	if err := os.Symlink(file, filepath.Join(dir, "link.go")); err != nil {
		t.Skipf("this filesystem has no symlinks: %v", err)
	}

	g := gateFor(t, config.Config{Mode: config.ModeManual}, dir)
	want := canonical(t, file)

	for _, spelling := range []string{"auth.go", "./auth.go", file, "link.go"} {
		got := g.request(call("edit", `{"path":`+quote(t, spelling)+`}`)).Path
		if got != want {
			t.Errorf("%q resolves to %q, want %q — every spelling of one file is one grant",
				spelling, got, want)
		}
	}
}

// TestTheUsersOverridesReachTheCompiledRules. `modes.<name>` is deep-merged onto
// the preset (decisions.md), and a session that compiled the preset alone would
// look right in every test that never wrote a config file.
func TestTheUsersOverridesReachTheCompiledRules(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		Mode: config.ModeManual,
		Modes: map[string]config.ModePermissions{
			config.ModeManual: {Edit: "allow", Bash: map[string]string{"go test*": "allow"}},
		},
	}
	g := gateFor(t, cfg, dir)

	for _, tc := range []struct {
		name string
		call llm.ToolCall
		asks bool
	}{
		{name: "the override the user wrote", call: call("edit", `{"path":"a.go"}`), asks: false},
		{name: "a pattern the user added", call: call("bash", `{"command":"go test ./..."}`), asks: false},
		// The preset's own answers survive the merge, or an override is a
		// replacement rather than a layer.
		{name: "what the preset still says", call: call("write", `{"path":"a.go"}`), asks: true},
		{name: "a command neither names", call: call("bash", `{"command":"curl example.com"}`), asks: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := g.Prompts(tc.call); got != tc.asks {
				t.Errorf("Prompts(%s) = %v, want %v", tc.call.Name, got, tc.asks)
			}
		})
	}
}

// TestAModeWithNoRulesRefusesToStart. Yolo is a bypass ahead of the ladder
// rather than a permissive preset (design §7.7 rung 0) and nothing arms it yet.
// A session that quietly ran under the manual rules instead would prompt while
// the status line said yolo, which teaches the user the wrong thing about what
// is guarding them.
func TestAModeWithNoRulesRefusesToStart(t *testing.T) {
	_, err := newGate(config.Config{Mode: config.ModeYolo}, nil, nil)
	if err == nil {
		t.Fatal("a session started in a mode with no rules to run under")
	}
	if !strings.Contains(err.Error(), config.ModeYolo) {
		t.Errorf("the refusal %q does not name the mode it is about", err)
	}
}

// TestARuleTheLadderCannotReadIsRefusedAtStartup: permission.Compile reports
// every fault at once, and the alternative to failing here is a denial at the
// moment a tool runs, naming the call rather than the line that broke it.
func TestARuleTheLadderCannotReadIsRefusedAtStartup(t *testing.T) {
	cfg := config.Config{
		Mode:  config.ModeManual,
		Modes: map[string]config.ModePermissions{config.ModeManual: {Edit: "maybe"}},
	}
	_, err := newGate(cfg, nil, nil)
	if err == nil {
		t.Fatal(`a mode whose edit rule is "maybe" was accepted`)
	}
	if !strings.Contains(err.Error(), "maybe") {
		t.Errorf("the refusal %q does not name what was written", err)
	}
}

// TestWithNothingToAskAnUnanswerableCallIsDenied is the headless path's shape.
// `rasp run -p` composes no prompter, so a question there has nobody to put it
// to — and the answer has to be a refusal that says so, never a wait for an
// answer that is not coming.
func TestWithNothingToAskAnUnanswerableCallIsDenied(t *testing.T) {
	g := gateFor(t, config.Config{Mode: config.ModeManual}, t.TempDir())
	g.service = permission.New(nil)
	if err := g.SetMode(permission.ModeManual); err != nil {
		t.Fatalf("SetMode: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), settle)
	defer cancel()

	err := g.Approve(ctx, call("write", `{"path":"a.go"}`))
	if !errors.Is(err, permission.ErrDenied) {
		t.Fatalf("a call nobody can be asked about answered %v, want a denial", err)
	}
	if ctx.Err() != nil {
		t.Error("the call waited for the whole timeout, so it was blocking on an answer nobody would give")
	}
}

// TestTheSessionPutsTheGateInFrontOfEveryTool is what every ticket before this
// one flagged and none could check: the composition itself. A registry with no
// tools, or an agent with no Approver, is a session that looks like it works —
// so this drives a real turn through the real loop and the real ladder, and
// fails if the write reaches the filesystem before anyone was asked.
func TestTheSessionPutsTheGateInFrontOfEveryTool(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "new.go")

	asked := make(chan permission.Request, 4)
	provider := fake.New(
		fake.Text("Writing it."),
		fake.ToolCall("write", `{"path":"new.go","content":"package demo\n"}`),
		fake.Done(llm.StopToolUse),

		fake.Text("Done."),
		fake.Done(llm.StopEndTurn),
	)

	cfg := config.Config{Mode: config.ModeManual, MaxOutputTokens: 1024}
	s, err := newSession(cfg, provider, "fake-model", dir, recordingPrompter{asked}, nil)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	defer s.ws.Close()

	turn := make(chan error, 1)
	go func() { turn <- s.agent.Send(context.Background(), "write new.go") }()

	var req permission.Request
	select {
	case req = <-asked:
	case err := <-turn:
		t.Fatalf("the turn finished without anybody being asked (%v); a gate that is not "+
			"installed is a session that edits files silently", err)
	case <-time.After(settle):
		t.Fatal("nothing was asked and the turn never finished")
	}

	if _, err := os.Stat(target); err == nil {
		t.Fatal("the file was written while the question was still open")
	}
	if req.Action != permission.ActionWrite || req.Path == "" {
		t.Errorf("the question was %+v; it should name the write and the file it lands on", req)
	}

	if !s.gate.Resolve(req.CallID, permission.DecisionOnce) {
		t.Fatal("the answer reached no open question, so the turn is still waiting")
	}
	select {
	case err := <-turn:
		if err != nil {
			t.Fatalf("the turn failed: %v", err)
		}
	case <-time.After(settle):
		t.Fatal("the turn never finished after the question was answered")
	}

	// Read back with os rather than through the workspace, so an assertion does
	// not go down the same path a confinement bug would.
	if _, err := os.Stat(target); err != nil {
		t.Errorf("the approved write never landed: %v", err)
	}

	// And the model was offered all eight, which is what puts the empty registry
	// this session replaced beyond reach.
	names := make([]string, 0, 8)
	for _, spec := range provider.Requests()[0].Tools {
		names = append(names, spec.Name)
	}
	if want := []string{"bash", "edit", "find", "grep", "ls", "read", "todos", "write"}; !slices.Equal(names, want) {
		t.Errorf("the session offers %v, want %v", names, want)
	}
}

// TestReadAndEditShareOneTracker. The read-before-edit guard refuses a file the
// session has not read, and what it consults is the tracker read recorded into
// (design §12) — so a registry that hands each tool a tracker of its own turns
// every edit into a refusal, however carefully the model reads the file first.
func TestReadAndEditShareOneTracker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.go"), []byte("package demo\n"), 0o644); err != nil {
		t.Fatalf("seeding the workspace: %v", err)
	}

	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatalf("opening the workspace at %s: %v", dir, err)
	}
	defer ws.Close()

	set := builtinTools(ws).Snapshot()
	read, ok := set.Get("read")
	if !ok {
		t.Fatal("the registry holds no read tool")
	}
	edit, ok := set.Get("edit")
	if !ok {
		t.Fatal("the registry holds no edit tool")
	}

	if res, err := read.Run(t.Context(), json.RawMessage(`{"path":"auth.go"}`)); err != nil || res.IsError {
		t.Fatalf("reading the file: %v %s", err, res.Content)
	}
	res, err := edit.Run(t.Context(), json.RawMessage(
		`{"path":"auth.go","old_string":"demo","new_string":"other"}`))
	if err != nil {
		t.Fatalf("editing the file the read recorded: %v", err)
	}
	if res.IsError {
		t.Errorf("the edit was refused after the read that should have satisfied it: %s", res.Content)
	}
}

// settle is how long a test waits for something that has already been asked
// for. Reaching it is a failure, never a slow machine.
const settle = 5 * time.Second

func gateFor(t *testing.T, cfg config.Config, dir string) *gate {
	t.Helper()

	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatalf("opening the workspace at %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := ws.Close(); err != nil {
			t.Errorf("closing the workspace: %v", err)
		}
	})

	g, err := newGate(cfg, ws, recordingPrompter{make(chan permission.Request, 8)})
	if err != nil {
		t.Fatalf("newGate: %v", err)
	}
	return g
}

// canonical is a path with its symlinks resolved, which is what the workspace
// answers with: on macOS a temp directory is reached through one, so /var and
// /private/var are the same file spelled two ways.
func canonical(t *testing.T, path string) string {
	t.Helper()

	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolving %s: %v", path, err)
	}
	return real
}

func call(name, input string) llm.ToolCall {
	return llm.ToolCall{ID: "call_" + name, Name: name, Input: json.RawMessage(input)}
}

func quote(t *testing.T, s string) string {
	t.Helper()

	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("encoding %q: %v", s, err)
	}
	return string(raw)
}

// recordingPrompter is the UI's half of rung 5: it publishes and returns,
// because the goroutine calling it is the one the tool call is blocked on.
type recordingPrompter struct{ asked chan permission.Request }

func (r recordingPrompter) Prompt(req permission.Request) {
	select {
	case r.asked <- req:
	default:
	}
}

var _ agent.Approver = (*gate)(nil)
