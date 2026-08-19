package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/theocod3s/rasp/internal/config"
	"github.com/theocod3s/rasp/internal/headless"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/anthropic"
	"github.com/theocod3s/rasp/internal/llm/openaicompat"
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
			runner := headless.Runner{
				Provider:  provider,
				Model:     model,
				MaxTokens: res.Config.MaxOutputTokens,
				Out:       cmd.OutOrStdout(),
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
//
// The cut is at the FIRST slash and the rest is the model as its own API knows it,
// which is what lets `openrouter/anthropic/claude-sonnet-4.5` name a provider whose
// model ids have slashes of their own.
func buildProvider(ctx context.Context, res *config.Result) (llm.Provider, string, error) {
	cfg := res.Config

	name, model, ok := strings.Cut(cfg.Model, "/")
	if !ok || name == "" || model == "" {
		return nil, "", fmt.Errorf("model %q is not provider/id, so there is nothing saying which "+
			"API to call; anthropic/claude-opus-5 is the shape", cfg.Model)
	}

	// What the file holds is a recipe — `$(op read …)`, `${VAR}` — and the resolver
	// is what turns it into a value. It also decides what NOT to expand: anything
	// that came from the environment or a flag has already been through a shell
	// (design §10). Sending the recipe is a 401 on every run.
	provider := cfg.Providers[name]
	expander := config.NewExpander(res, config.ExpanderOptions{})

	// No check that a credential exists: an empty key is left to the SDK's own
	// chain on purpose (decisions.md), and that chain has four sources a check
	// here could only enumerate incompletely — locking out whoever used the one it
	// forgot, in place of a refusal that names every source it tried.
	var key string
	if provider.APIKey != "" {
		var err error
		if key, err = expander.Expand(ctx, "providers."+name+".api_key"); err != nil {
			return nil, "", err
		}
	}

	baseURL := provider.BaseURL
	if baseURL != "" {
		// The grammar is per value, not per key: someone who writes `${GATEWAY}` in
		// one field has the same expectation in the other, and an unexpanded one
		// fails naming a URL they never wrote.
		expanded, err := expander.Expand(ctx, "providers."+name+".base_url")
		if err != nil {
			return nil, "", err
		}
		baseURL = expanded
	}

	// Anthropic is the one API with an adapter of its own; every other name is an
	// OpenAI-compatible endpoint, which is what the second adapter exists to be
	// (design §2). Nothing here checks the name against a list, because the list
	// would be the set of endpoints anyone has told us about — and the point of a
	// swappable base URL is that it is not one.
	if name == anthropic.ProviderID {
		return anthropic.New(anthropic.Config{APIKey: key, BaseURL: baseURL}), model, nil
	}
	return openaicompat.New(openaicompat.Config{
		ProviderID: name,
		APIKey:     key,
		BaseURL:    baseURL,
	}), model, nil
}
