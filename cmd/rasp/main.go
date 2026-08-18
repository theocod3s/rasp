// Command rasp is a coding agent for the terminal.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/theocod3s/rasp/internal/config"
	"github.com/theocod3s/rasp/internal/logx"
)

// version is injected at build time via -ldflags "-X main.version=...".
// See design §14.
var version = "dev"

func main() {
	os.Exit(execute(os.Args[1:]))
}

// execute runs the command tree with logging configured around it and returns
// the exit status. Separate from main because os.Exit skips deferred calls, and
// the log file has to be closed on the failing path too.
func execute(args []string) int {
	lg := logx.Init(nil)
	defer lg.Close()

	// Shown once, here, because a logger that quietly went nowhere is a
	// debugging session spent reading an empty file.
	for _, warning := range lg.Warnings {
		fmt.Fprintf(os.Stderr, "rasp: %s\n", warning)
	}

	// Cobra reads os.Args when handed a nil argument list, so a caller asking for
	// no arguments at all would get this process's own — a test binary's flags.
	if args == nil {
		args = []string{}
	}
	cmd := newRootCmd()
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		// Cobra has already written the error to stderr.
		return 1
	}
	return 0
}

// newRootCmd builds the command tree. The TUI and the session and mcp
// subcommands join it with the milestones that add them; until then the root
// says what it is and `run` is the whole of the agent.
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

	cmd.AddCommand(newConfigCmd(), newRunCmd())
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
