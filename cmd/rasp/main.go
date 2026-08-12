// Command rasp is a coding agent for the terminal.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/theocod3s/rasp/internal/config"
)

// version is injected at build time via -ldflags "-X main.version=...".
// See design §14.
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		// Cobra has already written the error to stderr.
		os.Exit(1)
	}
}

// newRootCmd builds the command tree. The TUI, `run` and the session and mcp
// subcommands join it with the milestones that add them; until then the root
// says what it is and the config subcommand is the whole of it.
func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rasp",
		Short:   "A coding agent for the terminal",
		Version: version,

		// A configuration error is not a usage error, and printing the whole
		// usage block after one buries the sentence that matters.
		SilenceUsage: true,

		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "rasp %s\n", version)
			return nil
		},
	}

	// Flags come from the config package's own table rather than being
	// declared here, so a flag cannot exist that the precedence chain does not
	// know how to place.
	for _, b := range config.FlagBindings() {
		cmd.PersistentFlags().String(b.Flag, "", b.Usage)
	}

	cmd.AddCommand(newConfigCmd())
	return cmd
}

// changedFlags reports the configuration flags the user actually set. A flag
// left at its default must not reach Load: it would outrank every file in the
// chain while carrying no instruction.
func changedFlags(cmd *cobra.Command) map[string]string {
	set := map[string]string{}
	for _, b := range config.FlagBindings() {
		if f := cmd.Flags().Lookup(b.Flag); f != nil && f.Changed {
			set[b.Flag] = f.Value.String()
		}
	}
	return set
}
