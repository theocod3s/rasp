package main

import (
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
			provider, model, err := buildProvider(res.Config)
			if err != nil {
				return err
			}
			runner := headless.Runner{
				Provider: provider,
				Model:    model,
				Out:      cmd.OutOrStdout(),
				Warn:     cmd.ErrOrStderr(),
			}
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
func buildProvider(cfg config.Config) (llm.Provider, string, error) {
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
	return anthropic.New(anthropic.Config{
		APIKey:  provider.APIKey,
		BaseURL: provider.BaseURL,
	}), model, nil
}
