package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/theocod3s/rasp/internal/config"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/workspace"
)

// gate is the permission service as both sides of a turn reach it: the loop
// asks whether a tool call may run, and the UI answers the question that
// opened. It is also the only place that knows what a tool call *is* — an
// action, a path, a command — which is the mapping neither of those two may
// hold, because it would mean knowing the other's vocabulary (decisions.md).
type gate struct {
	service *permission.Service
	ws      *workspace.Workspace

	// modes is the user's `modes.<name>` overrides, kept rather than read once
	// because a mode switch recompiles: reaching for the preset alone there
	// would drop the user's config the first time the mode changed.
	modes map[string]config.ModePermissions
}

// newGate builds the service around prompter and puts it in cfg's mode. A nil
// prompter is a session with nothing to ask, where the ladder denies at its last
// rung rather than blocking — which is what the headless path wants of it.
func newGate(cfg config.Config, ws *workspace.Workspace, prompter permission.Prompter) (*gate, error) {
	g := &gate{service: permission.New(prompter), ws: ws, modes: cfg.Modes}
	if err := g.SetMode(permission.Mode(cfg.Mode)); err != nil {
		return nil, err
	}
	return g, nil
}

// SetMode compiles the mode's preset with the user's overrides merged onto it
// and installs the result (design §7.4).
func (g *gate) SetMode(mode permission.Mode) error {
	preset, ok := permission.Presets()[mode]
	if !ok {
		// Yolo is the one name that reaches this: a bypass ahead of the ladder
		// rather than a permissive preset (design §7.7 rung 0), with nothing here
		// to arm it yet. Refused rather than quietly downgraded, because a session
		// that prompts while the status line says yolo teaches the user the wrong
		// thing about what is guarding them.
		return fmt.Errorf("mode %q has no permission rules to run under, so rasp would be "+
			"guessing what it may do; use plan, manual or auto", mode)
	}

	over := g.modes[string(mode)]
	rules, err := permission.Compile(permission.Merge(preset, permission.PermissionSet{
		Read:  permission.Rule(over.Read),
		Write: permission.Rule(over.Write),
		Edit:  permission.Rule(over.Edit),
		Fetch: permission.Rule(over.Fetch),
		Bash:  permission.Patterns(over.Bash),
		MCP:   permission.Patterns(over.MCP),
	}))
	if err != nil {
		return fmt.Errorf("the %s mode's permission rules: %w", mode, err)
	}
	g.service.SetRules(rules)
	return nil
}

// SetYolo arms or disarms the bypass ahead of the ladder (design §7.7). Nothing
// is compiled and nothing can fail: the mode's rules stay installed underneath,
// and disarming is the session going back under them.
func (g *gate) SetYolo(on bool) { g.service.SetYolo(on) }

func (g *gate) Prompts(call llm.ToolCall) bool { return g.service.Prompts(g.request(call)) }

func (g *gate) Approve(ctx context.Context, call llm.ToolCall) error {
	return g.service.Ask(ctx, g.request(call))
}

func (g *gate) Resolve(callID string, d permission.Decision) bool {
	return g.service.Resolve(callID, d)
}

// bashTool is the one builtin whose arguments carry a command line, and the
// only tool whose arguments may be read as one: a `command` field on anything
// else — an MCP server's own tool, say — would otherwise be matched against the
// bash pattern table, where `auto` allows almost everything (design §7.2).
const bashTool = "bash"

// actions is what each builtin does, in the vocabulary a permission set is
// written in (design §7.1). todos is here with the tools that only look because
// it keeps a list in memory and touches neither the filesystem nor a shell.
//
// A name that is not here is an MCP tool, which is the only other thing the
// registry holds, and those resolve against the MCP table under ActionExecute
// with the tool name as the string to match (design §8.2).
var actions = map[string]permission.Action{
	"read":   permission.ActionRead,
	"grep":   permission.ActionRead,
	"find":   permission.ActionRead,
	"ls":     permission.ActionRead,
	"todos":  permission.ActionRead,
	"edit":   permission.ActionEdit,
	"write":  permission.ActionWrite,
	bashTool: permission.ActionExecute,
}

// request is one tool call as the ladder reads it.
//
// Arguments that will not parse are left out rather than failing the call here:
// the tool is about to refuse them itself, with a message about the tool rather
// than about permissions.
func (g *gate) request(call llm.ToolCall) permission.Request {
	req := permission.Request{CallID: call.ID, Tool: call.Name, Action: permission.ActionExecute}
	if action, ok := actions[call.Name]; ok {
		req.Action = action
	}

	var args struct {
		Path    string `json:"path"`
		Command string `json:"command"`
	}
	_ = json.Unmarshal(call.Input, &args)

	if call.Name == bashTool {
		req.Command = args.Command
	}
	if req.Action != permission.ActionExecute {
		req.Path = g.path(args.Path)
	}
	return req
}

// path is what a grant is keyed on: absolute, and through symlinks, so `a.go`,
// `./a.go` and a link to it are one grant rather than three
// (permission/request.go).
//
// A path the workspace refuses — one pointing outside it — is carried through as
// the model wrote it. The tool will refuse it too, and keyed on the empty string
// one "always" would cover every path a later call could name.
func (g *gate) path(name string) string {
	if name == "" || g.ws == nil {
		return name
	}
	abs, err := g.ws.Abs(name)
	if err != nil {
		return name
	}
	// The file a write is creating has no realpath yet; the root is already
	// canonical, so this is what EvalSymlinks answers once it exists.
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}
