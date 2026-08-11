package config_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/config"
)

// TestUnknownKeysWarnAndAreDropped: the decoder ignores a key it does not
// recognise without a word, which turns one typo into a setting that reads as
// applied and is not. Warning is the right severity — a config written for a
// newer rasp still has to start — but silence is not.
func TestUnknownKeysWarnAndAreDropped(t *testing.T) {
	res := load(t, config.Sources{
		GlobalPath: global(t, `{
		  "modle": "typo at the top",
		  "providers": {"anthropic": {"api_ky": "typo further down"}}
		}`),
	})

	warned := map[string]bool{}
	for _, w := range res.Warnings {
		warned[w.Key] = true
	}
	for _, key := range []string{"modle", "providers.anthropic.api_ky"} {
		if !warned[key] {
			t.Errorf("%s drew no warning; got %v", key, res.Warnings)
		}
		if _, ok := res.Origins[key]; ok {
			t.Errorf("%s is listed as a resolved setting although rasp ignores it", key)
		}
	}
}

// TestUserDefinedKeysAreNotUnknown: half the schema is maps whose keys are the
// user's to invent — provider ids, MCP server names, bash globs. A check that
// flagged those would be worse than no check.
func TestUserDefinedKeysAreNotUnknown(t *testing.T) {
	res := load(t, config.Sources{
		GlobalPath: global(t, `{
		  "providers": {"my-gateway": {"base_url": "http://localhost:8080/v1"}},
		  "modes": {"manual": {"bash": {"just *": "allow"}}},
		  "mcp": {"servers": {"whatever": {"command": "npx", "env": {"TOKEN": "x"}}}}
		}`),
	})

	if len(res.Warnings) > 0 {
		t.Errorf("user-chosen keys were reported as unknown: %v", res.Warnings)
	}
	if got := res.Config.Providers["my-gateway"].BaseURL; got != "http://localhost:8080/v1" {
		t.Errorf("providers.my-gateway.base_url = %q, want it kept", got)
	}
}

// TestCredentialsAreHiddenWhenPrinted. `rasp config check` exists to say where
// a value came from, and its output lands in terminals, screen shares and CI
// logs. The origin is the part worth seeing; the key itself never is.
func TestCredentialsAreHiddenWhenPrinted(t *testing.T) {
	secrets := []string{
		"providers.anthropic.api_key",
		"providers.my-gateway.api_key",
		"mcp.servers.github.env.GITHUB_TOKEN",
	}
	for _, key := range secrets {
		if !config.IsSecret(key) {
			t.Errorf("IsSecret(%q) = false, want true", key)
		}
		if got := config.Display(key, "sk-ant-not-a-real-key"); strings.Contains(got, "sk-ant") {
			t.Errorf("Display(%q, …) = %q, want the value hidden", key, got)
		}
	}

	ordinary := []string{
		"model",
		"providers.anthropic.base_url",
		"mcp.servers.github.command",
		"modes.manual.bash.api_key", // a bash pattern, not a credential
	}
	for _, key := range ordinary {
		if config.IsSecret(key) {
			t.Errorf("IsSecret(%q) = true, want false", key)
		}
	}

	if got := config.Display("context.files", []any{"AGENTS.md"}); got != `["AGENTS.md"]` {
		t.Errorf("Display of an array = %q, want the JSON the user wrote", got)
	}
}

// TestSourcesAreReportedInPrecedenceOrder. `config check` prints them in the
// order they apply, and a reader working out why a value won reads that list
// top to bottom.
func TestSourcesAreReportedInPrecedenceOrder(t *testing.T) {
	res := load(t, config.Sources{})

	want := []config.Layer{
		config.LayerDefault,
		config.LayerGlobal,
		config.LayerProject,
		config.LayerEnv,
		config.LayerFlag,
	}
	var got []config.Layer
	for _, src := range res.Sources {
		got = append(got, src.Origin.Layer)
	}
	if !slices.Equal(got, want) {
		t.Errorf("sources = %v, want %v", got, want)
	}
}

// TestEmptySourcesSayWhatTheyLookedFor. "Why is my key not being picked up" is
// the question, and a source reporting only that it found nothing answers none
// of it.
func TestEmptySourcesSayWhatTheyLookedFor(t *testing.T) {
	res := load(t, config.Sources{})

	for _, src := range res.Sources {
		switch src.Origin.Layer {
		case config.LayerEnv:
			if !strings.Contains(src.Origin.Detail, "ANTHROPIC_API_KEY") {
				t.Errorf("env source = %q, want it to list the variables it read", src.Origin.Detail)
			}
		case config.LayerFlag:
			if !strings.Contains(src.Origin.Detail, "--model") {
				t.Errorf("flag source = %q, want it to list the flags it read", src.Origin.Detail)
			}
		}
	}
}

// TestGlobalPathFollowsXDG. design §10 names ~/.config/rasp/config.json
// outright, which os.UserConfigDir would not produce on macOS.
func TestGlobalPathFollowsXDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	got, err := config.GlobalPath()
	if err != nil {
		t.Fatalf("GlobalPath: %v", err)
	}
	if want := filepath.Join(dir, "rasp", "config.json"); got != want {
		t.Errorf("GlobalPath = %q, want %q", got, want)
	}
}

func TestGlobalPathFallsBackToDotConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	got, err := config.GlobalPath()
	if err != nil {
		t.Fatalf("GlobalPath: %v", err)
	}
	if want := filepath.Join(".config", "rasp", "config.json"); !strings.HasSuffix(got, want) {
		t.Errorf("GlobalPath = %q, want it to end in %q", got, want)
	}
}
