package config_test

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/config"
)

// TestPrecedencePerHop walks design §10's chain one rung at a time: built-in
// defaults, global file, project file, environment, flags. Each case adds
// exactly one layer to the one below it and asserts that the new layer wins,
// so a broken rung names itself instead of being masked by the rung above.
func TestPrecedencePerHop(t *testing.T) {
	globalPath := global(t, `{"model": "from/global"}`)
	projectDir := project(t, `{"model": "from/project"}`)

	tests := []struct {
		name   string
		src    config.Sources
		want   string
		origin config.Origin
	}{
		{
			name: "defaults answer when nothing else is set",
			src:  config.Sources{},
			want: "anthropic/claude-opus-5",
			origin: config.Origin{
				Layer: config.LayerDefault,
			},
		},
		{
			name: "the global file beats the defaults",
			src:  config.Sources{GlobalPath: globalPath},
			want: "from/global",
			origin: config.Origin{
				Layer:  config.LayerGlobal,
				Detail: globalPath,
			},
		},
		{
			name: "the project file beats the global file",
			src:  config.Sources{GlobalPath: globalPath, ProjectDir: projectDir},
			want: "from/project",
			origin: config.Origin{
				Layer:  config.LayerProject,
				Detail: filepath.Join(projectDir, ".rasp", config.File),
			},
		},
		{
			name: "the environment beats the project file",
			src: config.Sources{
				GlobalPath: globalPath,
				ProjectDir: projectDir,
				Getenv:     env{"RASP_MODEL": "from/env"}.lookup,
			},
			want: "from/env",
			origin: config.Origin{
				Layer:  config.LayerEnv,
				Detail: "RASP_MODEL",
			},
		},
		{
			name: "a flag beats the environment",
			src: config.Sources{
				GlobalPath: globalPath,
				ProjectDir: projectDir,
				Getenv:     env{"RASP_MODEL": "from/env"}.lookup,
				Flags:      map[string]string{"model": "from/flag"},
			},
			want: "from/flag",
			origin: config.Origin{
				Layer:  config.LayerFlag,
				Detail: "model",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := load(t, tc.src)
			if res.Config.Model != tc.want {
				t.Errorf("model = %q, want %q", res.Config.Model, tc.want)
			}
			if got := res.Origins["model"]; got != tc.origin {
				t.Errorf("origin of model = %+v, want %+v", got, tc.origin)
			}
		})
	}
}

// TestMergeIsPerKey is the reason the chain merges rather than replaces: a
// project config overriding one provider's key must not delete the rest of the
// providers table, or every repo would have to restate the global config to
// change one line (design §10).
func TestMergeIsPerKey(t *testing.T) {
	globalPath := global(t, `{
	  "model": "from/global",
	  "providers": {
	    "anthropic": {"api_key": "global-key"},
	    "openrouter": {"base_url": "https://openrouter.ai/api/v1", "models": ["a", "b"]}
	  }
	}`)
	projectDir := project(t, `{
	  "providers": {
	    "openrouter": {"models": ["c"]}
	  }
	}`)

	res := load(t, config.Sources{GlobalPath: globalPath, ProjectDir: projectDir})

	if got := res.Config.Model; got != "from/global" {
		t.Errorf("model = %q, want the global value to survive an unrelated project key", got)
	}
	if got := res.Config.Providers["anthropic"].APIKey; got != "global-key" {
		t.Errorf("providers.anthropic.api_key = %q, want the sibling provider untouched", got)
	}
	if got := res.Config.Providers["openrouter"].BaseURL; got != "https://openrouter.ai/api/v1" {
		t.Errorf("providers.openrouter.base_url = %q, want the sibling key untouched", got)
	}

	// An array is a value, not something to concatenate. Narrowing `models` to
	// one entry has to mean one entry.
	if got := res.Config.Providers["openrouter"].Models; !slices.Equal(got, []string{"c"}) {
		t.Errorf("providers.openrouter.models = %v, want the project's list to replace the global one", got)
	}

	// The origins split at the same granularity as the merge.
	projectPath := filepath.Join(projectDir, ".rasp", config.File)
	for key, want := range map[string]config.Origin{
		"model":                         {Layer: config.LayerGlobal, Detail: globalPath},
		"providers.anthropic.api_key":   {Layer: config.LayerGlobal, Detail: globalPath},
		"providers.openrouter.base_url": {Layer: config.LayerGlobal, Detail: globalPath},
		"providers.openrouter.models":   {Layer: config.LayerProject, Detail: projectPath},
	} {
		if got := res.Origins[key]; got != want {
			t.Errorf("origin of %s = %+v, want %+v", key, got, want)
		}
	}
}

// TestEmptyEnvVarIsUnset keeps an accidentally-exported empty variable from
// outranking a file somebody actually wrote.
func TestEmptyEnvVarIsUnset(t *testing.T) {
	res := load(t, config.Sources{
		ProjectDir: project(t, `{"model": "from/project"}`),
		Getenv:     env{"RASP_MODEL": ""}.lookup,
	})

	if got := res.Config.Model; got != "from/project" {
		t.Errorf("model = %q, want the project value to stand against an empty RASP_MODEL", got)
	}
	if got := res.Origins["model"].Layer; got != config.LayerProject {
		t.Errorf("origin layer = %v, want %v", got, config.LayerProject)
	}
}

// TestUnknownFlagIsRejected closes the gap between the flag table and the
// command line. A flag nobody can place is a flag that silently does nothing.
func TestUnknownFlagIsRejected(t *testing.T) {
	_, err := config.Load(config.Sources{
		GlobalPath: filepath.Join(t.TempDir(), config.File),
		ProjectDir: t.TempDir(),
		Getenv:     env{}.lookup,
		Flags:      map[string]string{"colour": "green"},
	})
	if err == nil {
		t.Fatal("Load accepted a flag that maps to no configuration key")
	}
}

// TestMissingFilesAreOrdinary: most people have no project config and many
// have no global one. Neither is an error, and both are reported so `config
// check` can say which file it did not find.
func TestMissingFilesAreOrdinary(t *testing.T) {
	res := load(t, config.Sources{})

	for _, want := range []config.Layer{config.LayerGlobal, config.LayerProject} {
		i := slices.IndexFunc(res.Sources, func(s config.Source) bool {
			return s.Origin.Layer == want
		})
		if i < 0 {
			t.Fatalf("no source reported for the %v layer", want)
		}
		if src := res.Sources[i]; src.Loaded || src.Note == "" {
			t.Errorf("%v source = %+v, want it reported as absent with a reason", want, src)
		}
	}
}

// TestEveryResolvedValueHasAnOrigin is the acceptance criterion behind `rasp
// config check`, checked against the resolved Config rather than against the
// report: every value that survives into the struct can name where it came
// from, and nothing is left over pointing at a value that did not.
func TestEveryResolvedValueHasAnOrigin(t *testing.T) {
	res := load(t, config.Sources{
		GlobalPath: global(t, `{
		  "small_model": "anthropic/claude-haiku-4-5",
		  "providers": {"anthropic": {"api_key": "$(op read op://vault/anthropic/key)"}},
		  "ui": {"theme": "dark"}
		}`),
		ProjectDir: project(t, `{
		  "modes": {"manual": {"bash": {"go test*": "allow", "rm *.go": "ask"}}},
		  "mcp": {"servers": {"github": {"command": "npx", "args": ["-y", "server"], "timeout": "10s"}}},
		  "context": {"files": ["AGENTS.md"]}
		}`),
		Getenv: env{"ANTHROPIC_API_KEY": "from-env"}.lookup,
		Flags:  map[string]string{"mode": "auto"},
	})

	for _, key := range configPaths(t, res.Config) {
		if _, ok := res.Origins[key]; !ok {
			t.Errorf("%s resolved to a value with no origin", key)
		}
	}
	for _, key := range res.Origins.Paths() {
		if _, ok := res.Values[key]; !ok {
			t.Errorf("%s has an origin but no resolved value", key)
		}
	}

	// A pattern key containing the path separator has to stay one key.
	if _, ok := res.Origins[`modes.manual.bash.rm *\.go`]; !ok {
		t.Errorf("a bash pattern containing a dot lost its own origin; got %v", res.Origins.Paths())
	}
}

// configPaths returns the key path of every value in cfg, by asking the
// encoder — so the test tracks what the struct actually holds rather than a
// list somebody has to remember to update.
func configPaths(t *testing.T, cfg config.Config) []string {
	t.Helper()

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshalling the resolved config: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decoding the resolved config: %v", err)
	}

	var (
		paths []string
		walk  func(val any, path string)
	)
	walk = func(val any, path string) {
		sub, isObj := val.(map[string]any)
		if !isObj || len(sub) == 0 {
			if path != "" {
				paths = append(paths, path)
			}
			return
		}
		for key, child := range sub {
			escaped := escapeKey.Replace(key)
			if path != "" {
				escaped = path + "." + escaped
			}
			walk(child, escaped)
		}
	}
	walk(decoded, "")

	slices.Sort(paths)
	return paths
}

// escapeKey mirrors the escaping the package applies when it joins a key path,
// so the two agree on where one key ends and the next begins.
var escapeKey = strings.NewReplacer(`\`, `\\`, `.`, `\.`)
