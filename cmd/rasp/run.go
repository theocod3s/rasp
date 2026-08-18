package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/theocod3s/rasp/internal/config"
	"github.com/theocod3s/rasp/internal/headless"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/anthropic"
)

const promptFlag = "prompt"

func newRunCmd() *cobra.Command {
	var prompt string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Answer one prompt and exit",
		Long: "Answer one prompt and exit, streaming the reply to stdout as it arrives.\n\n" +
			"Only the reply reaches stdout, so the output is a script's to consume;\n" +
			"anything else rasp has to say goes to stderr, and any failure exits 1.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(prompt) == "" {
				return fmt.Errorf("--%s is empty; it carries the prompt to answer", promptFlag)
			}
			res, err := config.Load(config.Sources{Flags: changedFlags(cmd)})
			if err != nil {
				return err
			}
			// On stderr, where the reply is not: a setting that did not apply is
			// what the user needs to see when the answer looks wrong.
			for _, warning := range res.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "rasp: %s\n", warning)
			}
			provider, model, err := buildProvider(cmd.Context(), res)
			if err != nil {
				return err
			}
			runner := headless.Runner{Provider: provider, Model: model, Out: cmd.OutOrStdout()}
			return runner.Run(cmd.Context(), prompt)
		},
	}

	cmd.Flags().StringVarP(&prompt, promptFlag, "p", "", "the prompt to answer (required)")
	// Errors only on a name no flag has, and the line above defines it.
	_ = cmd.MarkFlagRequired(promptFlag)
	return cmd
}

// buildProvider resolves the configured model id into the adapter that serves it
// and the id that adapter puts on the wire. A model id carries its provider —
// `anthropic/claude-opus-5` — which is why there is no --provider flag to
// contradict it (design §10).
func buildProvider(ctx context.Context, res *config.Result) (llm.Provider, string, error) {
	cfg := res.Config

	name, model, ok := strings.Cut(cfg.Model, "/")
	if !ok || name == "" || model == "" {
		return nil, "", fmt.Errorf("model %q is not provider/id, so there is nothing saying which "+
			"API to call; anthropic/claude-opus-5 is the shape", cfg.Model)
	}
	if name != anthropic.ProviderID {
		return nil, "", fmt.Errorf("no adapter for provider %q; rasp speaks %q so far",
			name, anthropic.ProviderID)
	}

	provider := cfg.Providers[name]
	if provider.APIKey == "" {
		// Checked here rather than left to a 401, whose answer names neither of the
		// two places a key comes from.
		return nil, "", errors.New("no API key for anthropic; set ANTHROPIC_API_KEY or " +
			`providers.anthropic.api_key in the config file`)
	}
	// What the file holds is a recipe — `$(op read …)`, `${VAR}` — and the resolver
	// is what turns it into a value. It also decides what NOT to expand: anything
	// that came from the environment or a flag has already been through a shell
	// (design §10). Sending the recipe is a 401 on every run.
	expander := config.NewExpander(res, config.ExpanderOptions{})
	key, err := expander.Expand(ctx, "providers."+name+".api_key")
	if err != nil {
		return nil, "", err
	}
	baseURL := provider.BaseURL
	if baseURL != "" {
		// The grammar is per value, not per key: someone who writes `${GATEWAY}` in
		// one field has the same expectation in the other, and an unexpanded one
		// fails naming a URL they never wrote.
		if baseURL, err = expander.Expand(ctx, "providers."+name+".base_url"); err != nil {
			return nil, "", err
		}
	}
	return anthropic.New(anthropic.Config{APIKey: key, BaseURL: baseURL}), model, nil
}
