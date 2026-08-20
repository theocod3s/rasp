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

	provider, model, err := buildProvider(cmd.Context(), res)
	if err != nil {
		return err
	}

	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("the working directory is the workspace every tool is confined to, "+
			"and it could not be read: %w", err)
	}

	// The configured id rather than the one buildProvider puts on the wire: the
	// wire id has had its provider cut off the front, and `claude-opus-5` served
	// through a router says less than the line the user wrote.
	ui := tui.New(tui.Config{
		Model: res.Config.Model,
		Mode:  permission.Mode(res.Config.Mode),
	})
	s, err := newSession(res.Config, provider, model, dir, ui, ui.Events)
	if err != nil {
		return err
	}
	defer func() { _ = s.ws.Close() }()

	return ui.Run(s.agent, s.gate)
}

// session is what a frontend needs to run turns: the agent, the gate it answers
// permission questions through, and the workspace the tools are confined to.
type session struct {
	agent *agent.Agent
	gate  *gate
	ws    *workspace.Workspace
}

// newSession composes the loop, the tools, the workspace and the permission
// gate into one another. It takes the two seams a frontend sits on either side
// of — where a question the ladder could not answer is published, and where an
// event is drawn — rather than the frontend itself, so the composition can be
// driven without a terminal.
//
// Nothing here is optional. A registry with no tools, or an agent with no
// Approver, is a session that looks like it works: the first edits files with
// nobody asked, and the second cannot edit anything at all.
func newSession(cfg config.Config, provider llm.Provider, model, dir string,
	prompter permission.Prompter, events func(agent.Event)) (*session, error) {
	ws, err := workspace.Open(dir)
	if err != nil {
		return nil, err
	}

	g, err := newGate(cfg, ws, prompter)
	if err != nil {
		ws.Close()
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
		ws.Close()
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
