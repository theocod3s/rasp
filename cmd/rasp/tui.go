package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/config"
	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tui"
)

// startTUI is what `rasp` on its own does: resolve the configuration, build the
// provider it names, and give the UI an agent to run turns against.
//
// The tool registry is empty. The tools are written and the loop runs them, but
// the permission gate that has to stand in front of them is not wired yet — and
// an interactive session that edits files and runs commands without ever asking
// is the one thing this must not ship by accident. They join the UI with the
// gate, not before it.
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

	// The configured id rather than the one buildProvider puts on the wire: the
	// wire id has had its provider cut off the front, and `claude-opus-5` served
	// through a router says less than the line the user wrote.
	ui := tui.New(tui.Config{
		Model: res.Config.Model,
		Mode:  permission.Mode(res.Config.Mode),
	})
	a, err := agent.New(agent.Config{
		Provider:  provider,
		Tools:     tool.NewRegistry(nil),
		Model:     model,
		MaxTokens: res.Config.MaxOutputTokens,
		Events:    ui.Events,
	})
	if err != nil {
		return err
	}
	return ui.Run(a)
}
