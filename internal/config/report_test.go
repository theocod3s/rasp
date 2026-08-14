package config_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/config"
)

// TestUnknownKeysWarnAndAreDropped.
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

// TestAnUnknownObjectNamesItsFile. A key holding an object has no origin entry
// of its own, and falling back to the zero Origin reads as "built-in default" —
// sending the reader at rasp's compiled-in defaults instead of their own file.
func TestAnUnknownObjectNamesItsFile(t *testing.T) {
	dir := project(t, `{"tools": {"foo": 1}}`)
	res := load(t, config.Sources{ProjectDir: dir})

	var found bool
	for _, w := range res.Warnings {
		if w.Key != "tools" {
			continue
		}
		found = true
		if want := filepath.Join(dir, ".rasp", config.File); w.Origin.Detail != want {
			t.Errorf("warning origin = %q, want %q", w.Origin, want)
		}
	}
	if !found {
		t.Errorf("no warning for the unknown object; got %v", res.Warnings)
	}
}

// TestAnEmptyObjectLaterFilledLeavesNoPhantom, which `rasp config check` would
// draw as a row reading `providers  null`.
func TestAnEmptyObjectLaterFilledLeavesNoPhantom(t *testing.T) {
	res := load(t, config.Sources{
		GlobalPath: global(t, `{"providers": {}}`),
		Getenv:     env{"ANTHROPIC_API_KEY": "from-env"}.lookup,
	})

	if _, ok := res.Origins["providers"]; ok {
		t.Error("providers kept an origin after a later layer filled it")
	}
	if _, ok := res.Origins["providers.anthropic.api_key"]; !ok {
		t.Error("the value that filled it has no origin")
	}
	for _, key := range res.Origins.Paths() {
		if _, ok := res.Values[key]; !ok {
			t.Errorf("%s has an origin but no resolved value", key)
		}
	}
}

// TestAnEmptyObjectThatStaysEmptyKeepsAnOrigin is the other half.
func TestAnEmptyObjectThatStaysEmptyKeepsAnOrigin(t *testing.T) {
	path := global(t, `{"providers": {}}`)
	res := load(t, config.Sources{GlobalPath: path})

	origin, ok := res.Origins["providers"]
	if !ok {
		t.Fatalf("an empty object lost its origin; got %v", res.Origins.Paths())
	}
	if origin.Detail != path {
		t.Errorf("origin = %v, want %s", origin, path)
	}

	// The precedence rule holds for it too: a later layer restating `{}` set it.
	projectDir := project(t, `{"providers": {}}`)
	res = load(t, config.Sources{GlobalPath: path, ProjectDir: projectDir})

	want := filepath.Join(projectDir, ".rasp", config.File)
	if got := res.Origins["providers"]; got.Detail != want {
		t.Errorf("origin = %v, want the project file %s", got, want)
	}
}

// TestAValueOfTheWrongSortNamesItsFile. encoding/json reports this against a Go
// field name — "Go struct field MCP.mcp.max_total_tools" — and never says which
// of the four sources wrote it.
func TestAValueOfTheWrongSortNamesItsFile(t *testing.T) {
	tests := []struct {
		name string
		file string
		want string
	}{
		{
			name: "a string where a number belongs",
			file: `{"mcp": {"max_total_tools": "sixty"}}`,
			want: "mcp.max_total_tools: want a whole number, got a string",
		},
		{
			name: "a string where an array belongs, under a map key",
			file: `{"providers": {"anthropic": {"models": "opus"}}}`,
			want: "providers.anthropic.models: want an array, got a string",
		},
		{
			name: "an object where a string belongs",
			file: `{"model": {"name": "a/b"}}`,
			want: "model: want a string, got an object",
		},
		{
			// Well-formed JSON and a number, so only a check that knows the
			// field is an integer catches it.
			name: "a fractional number where a whole one belongs",
			file: `{"context": {"reserve_tokens": 16.5}}`,
			want: "context.reserve_tokens: want a whole number, got 16.5",
		},
		{
			name: "a number past what an int can hold",
			file: `{"mcp": {"max_total_tools": 99999999999999999999}}`,
			want: "mcp.max_total_tools: want a whole number, got 99999999999999999999",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := project(t, tc.file)
			_, err := config.Load(config.Sources{
				GlobalPath: filepath.Join(t.TempDir(), config.File),
				ProjectDir: dir,
				Getenv:     env{}.lookup,
			})
			if err == nil {
				t.Fatal("Load accepted a value Config cannot hold")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
			if path := filepath.Join(dir, ".rasp", config.File); !strings.Contains(err.Error(), path) {
				t.Errorf("error does not name the file it came from:\n%s", err)
			}
		})
	}
}

// TestNullMeansNotSet.
func TestNullMeansNotSet(t *testing.T) {
	res := load(t, config.Sources{
		GlobalPath: global(t, `{
		  "mode": null,
		  "model": null,
		  "context": {"files": null},
		  "providers": {"anthropic": {"api_key": null}}
		}`),
	})

	if len(res.Warnings) > 0 {
		t.Errorf("a null drew a warning: %v", res.Warnings)
	}
	if got := res.Config.Mode; got != config.ModeManual {
		t.Errorf("mode = %q, want the built-in default to survive a null", got)
	}
	if got := res.Config.Model; got != "anthropic/claude-opus-5" {
		t.Errorf("model = %q, want the built-in default to survive a null", got)
	}
	if got := res.Config.Context.Files; len(got) != 2 {
		t.Errorf("context.files = %v, want the built-in default to survive a null", got)
	}
	if _, ok := res.Origins["providers.anthropic.api_key"]; ok {
		t.Error("a null created a setting that was never set")
	}
}

// TestATypoedModeNameIsReported. A typo here lands on the permission table,
// where believing a deny is in force and being wrong is the expensive direction.
func TestATypoedModeNameIsReported(t *testing.T) {
	res := load(t, config.Sources{
		GlobalPath: global(t, `{"modes": {
		  "manaul": {"bash": {"rm *": "deny"}},
		  "plan": {"bash": {"just *": "allow"}}
		}}`),
	})

	var warned bool
	for _, w := range res.Warnings {
		if w.Key == "modes.manaul" {
			warned = true
		}
	}
	if !warned {
		t.Errorf("modes.manaul drew no warning; got %v", res.Warnings)
	}
	if _, ok := res.Config.Modes["manaul"]; ok {
		t.Error("modes.manaul survived into the resolved config")
	}
	if got := res.Config.Modes["plan"].Bash["just *"]; got != "allow" {
		t.Errorf("modes.plan.bash[just *] = %q, want the real mode untouched", got)
	}
}

// TestUserDefinedKeysAreNotUnknown: half the schema is maps whose keys are the
// user's to invent.
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

// TestCredentialsAreHiddenWhenPrinted. The report lands in terminals, screen
// shares and CI logs.
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

// TestSourcesAreReportedInPrecedenceOrder: a reader working out why a value won
// reads the list top to bottom.
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

// TestEmptySourcesSayWhatTheyLookedFor.
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

// TestGlobalPathFollowsXDG, which os.UserConfigDir would not produce on macOS.
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
