package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/theocod3s/rasp/internal/config"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect the resolved configuration",
	}
	cmd.AddCommand(newConfigPathCmd(), newConfigCheckCmd())
	return cmd
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the config files rasp reads",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			global, err := config.GlobalPath()
			if err != nil {
				return err
			}
			project, err := config.ProjectPath("")
			if err != nil {
				return err
			}

			w := newTable(cmd.OutOrStdout())
			fmt.Fprintf(w, "global\t%s\n", global)
			fmt.Fprintf(w, "project\t%s\n", project)
			return w.Flush()
		},
	}
}

func newConfigCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Resolve the configuration and say where every value came from",
		Long: "Resolve the configuration through the full precedence chain and print the\n" +
			"result, naming the origin of every value: the built-in defaults, the global\n" +
			"file, the project file, an environment variable or a flag.\n\n" +
			"Credentials are printed as " + config.Hidden + ". Their origin is the part worth seeing,\n" +
			"and this output ends up in terminals, transcripts and CI logs.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := config.Load(config.Sources{Flags: changedFlags(cmd)})
			if err != nil {
				return err
			}
			printResult(cmd.OutOrStdout(), cmd.ErrOrStderr(), res)
			return nil
		},
	}
}

// printResult writes the report: where rasp looked, what it resolved, and — on
// stderr, so the table stays pipe-clean — what it thought was odd.
func printResult(stdout, stderr io.Writer, res *config.Result) {
	w := newTable(stdout)

	fmt.Fprintln(w, "sources")
	for _, src := range res.Sources {
		detail := src.Origin.Detail
		if detail == "" {
			// Only the defaults layer has nowhere to point at.
			detail = "compiled in"
		}
		status := "loaded"
		if !src.Loaded {
			status = src.Note
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\n", src.Origin.Layer, detail, status)
	}

	fmt.Fprintln(w, "\nsettings")
	for _, key := range res.Origins.Paths() {
		fmt.Fprintf(w, "  %s\t%s\t%s\n",
			key, config.Display(key, res.Values[key]), res.Origins[key])
	}
	w.Flush()

	if len(res.Warnings) > 0 {
		fmt.Fprintln(stderr, "\nwarnings")
		for _, warn := range res.Warnings {
			fmt.Fprintf(stderr, "  %s\n", warn)
		}
	}
}

// newTable returns a writer that aligns tab-separated columns.
func newTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}
