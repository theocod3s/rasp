package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/config"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tool/builtin"
	"github.com/theocod3s/rasp/internal/tui"
	"github.com/theocod3s/rasp/internal/workspace"
)

// startTUI is what `rasp` on its own does: resolve the configuration, build the
// provider it names, and give the UI an agent to run turns against.
func startTUI(cmd *cobra.Command) error {
	res, err := config.Load(config.Sources{Flags: changedFlags(cmd)})
	if err != nil {
		return err
	}
	// On stderr and before the UI owns the screen: a setting that did not apply
	// is what the user needs when the answers look wrong.
	for _, warning := range res.Warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "rasp: %s\n", warning)
	}

	// Ahead of everything slower, so a flag this cannot read stops the run before
	// a provider is built or a workspace opened.
	uiCfg, err := uiConfig(cmd, res.Config)
	if err != nil {
		return err
	}

	provider, model, err := buildProvider(cmd.Context(), res)
	if err != nil {
		return err
	}
	// One object handed to both, or the picker writes a level to something the
	// agent's requests never pass through.
	effort := newDepth(provider)
	uiCfg.Depth = effort

	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("the working directory is the workspace every tool is confined to, "+
			"and it could not be read: %w", err)
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		return err
	}
	// The banner's cwd row and the tools' own confinement read the same resolved
	// root, so a launch directory that is itself a symlink is never shown one way
	// and enforced another.
	uiCfg.Cwd = ws.Root()

	ui := tui.New(uiCfg)
	s, err := newSession(res.Config, effort, model, ws, ui, ui.Events)
	if err != nil {
		ws.Close()
		return err
	}
	defer func() { _ = s.ws.Close() }()

	return ui.Run(s.agent, s.gate)
}

// uiConfig is the session as the UI has to say it. The model is the configured
// id rather than the one buildProvider puts on the wire: that one has had its
// provider cut off the front, and `claude-opus-5` served through a router says
// less than the line the user wrote.
//
// --yolo is read here rather than resolved with everything else because it is
// not a configuration value (design §10) — it arms the bypass for this run and
// is written nowhere. The only way the read errors is the flag having been
// renamed out from under this name, and a session that quietly started gated
// when the user asked for --yolo is a wrong answer given without a word.
func uiConfig(cmd *cobra.Command, cfg config.Config) (tui.Config, error) {
	yolo, err := cmd.Flags().GetBool(yoloFlag)
	if err != nil {
		return tui.Config{}, fmt.Errorf("--%s decides whether this run asks before it does "+
			"anything, and it could not be read: %w", yoloFlag, err)
	}
	return tui.Config{
		Model:   cfg.Model,
		Mode:    permission.Mode(cfg.Mode),
		Version: version,
		Yolo:    yolo,
	}, nil
}

// session is what a frontend needs to run turns: the agent, the gate it answers
// permission questions through, and the workspace the tools are confined to.
type session struct {
	agent *agent.Agent
	gate  *gate
	ws    *workspace.Workspace
}

// newSession composes the loop, the tools and the permission gate onto an
// already-open workspace. It takes the two seams a frontend sits on either
// side of — where a question the ladder could not answer is published, and
// where an event is drawn — rather than the frontend itself, so the
// composition can be driven without a terminal.
//
// ws arrives open rather than as a directory to open because a caller may need
// its resolved root — the banner's cwd row — before the rest of the session
// exists to hand it back. Closing it is the caller's job on every path,
// including one where this returns an error.
//
// Nothing else here is optional. A registry with no tools, or an agent with no
// Approver, is a session that looks like it works: the first edits files with
// nobody asked, and the second cannot edit anything at all.
func newSession(cfg config.Config, provider llm.Provider, model string, ws *workspace.Workspace,
	prompter permission.Prompter, events func(agent.Event)) (*session, error) {
	g, err := newGate(cfg, ws, prompter)
	if err != nil {
		return nil, err
	}

	a, err := agent.New(agent.Config{
		Provider:  provider,
		Tools:     builtinTools(ws),
		Model:     model,
		MaxTokens: cfg.MaxOutputTokens,
		Approver:  g,
		Events:    events,
	})
	if err != nil {
		return nil, err
	}
	return &session{agent: a, gate: g, ws: ws}, nil
}

// builtinTools is the eight, all confined to ws. One tracker, shared by every
// tool that reads or mutates a file: the read-before-edit guard only sees a
// file the session read if edit and write consult the tracker read recorded
// into (design §5, step 17).
func builtinTools(ws *workspace.Workspace) *tool.Registry {
	reads := workspace.NewTracker()
	return tool.NewRegistry([]tool.Tool{
		builtin.NewBash(ws.Root()),
		builtin.Edit(ws, reads),
		builtin.NewFind(ws),
		builtin.NewGrep(ws, builtin.RipgrepPath()),
		builtin.NewLs(ws),
		builtin.NewRead(ws, reads),
		builtin.NewTodos(),
		builtin.NewWrite(ws, reads),
	})
}
