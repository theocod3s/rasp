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

// TestAnUnknownObjectNamesItsFile. Origins are recorded on leaves, so a key
// holding an object has no entry of its own — and a warning that falls back to
// the zero Origin reads as "built-in default", sending the reader at rasp's
// compiled-in defaults instead of the file they wrote.
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

// TestAnEmptyObjectLaterFilledLeavesNoPhantom. An object carries an origin of
// its own only while it is empty, since there is no leaf to hang one on. Once
// a later layer fills it, that entry describes a value nothing prints — and
// `rasp config check` draws it as a row reading `providers  null`.
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

// TestAnEmptyObjectThatStaysEmptyKeepsAnOrigin is the other half: `"providers":
// {}` says something, and it is the one shape with no leaf to say it through.
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

	// And the precedence rule holds for it like anything else: a later layer
	// restating `{}` is the layer that set it.
	projectDir := project(t, `{"providers": {}}`)
	res = load(t, config.Sources{GlobalPath: path, ProjectDir: projectDir})

	want := filepath.Join(projectDir, ".rasp", config.File)
	if got := res.Origins["providers"]; got.Detail != want {
		t.Errorf("origin = %v, want the project file %s", got, want)
	}
}

// TestAValueOfTheWrongSortNamesItsFile. encoding/json would report this
// against a Go field name and lose the map key entirely — "Go struct field
// MCP.mcp.max_total_tools" — and would never say which of the four sources
// wrote it. Naming the file is the whole reason this package exists.
func TestAValueOfTheWrongSortNamesItsFile(t *testing.T) {
	tests := []struct {
		name string
		file string
		want string
	}{
		{
			name: "a string where a number belongs",
			file: `{"mcp": {"max_total_tools": "sixty"}}`,
			want: "mcp.max_total_tools: want a number, got a string",
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

// TestNullIsAcceptedAnywhere. JSON allows null in any position and the decoder
// reads it as a zero value, so the shape check must not be stricter than the
// decoder it stands in front of.
func TestNullIsAcceptedAnywhere(t *testing.T) {
	res := load(t, config.Sources{
		GlobalPath: global(t, `{"model": null, "context": {"files": null}}`),
	})
	if len(res.Warnings) > 0 {
		t.Errorf("a null drew a warning: %v", res.Warnings)
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
