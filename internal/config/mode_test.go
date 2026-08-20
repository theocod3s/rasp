package config_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/config"
)

// TestProjectYoloIsRejected is design §10's first constraint on `mode`: a
// repository that could set yolo could disable every approval prompt on a
// stranger's machine.
func TestProjectYoloIsRejected(t *testing.T) {
	dir := project(t, `{"mode": "yolo"}`)
	path := filepath.Join(dir, ".rasp", config.File)

	_, err := config.Load(config.Sources{
		GlobalPath: filepath.Join(t.TempDir(), config.File),
		ProjectDir: dir,
		Getenv:     env{}.lookup,
	})
	if err == nil {
		t.Fatal(`Load accepted "mode": "yolo" from a project config`)
	}

	// Which file, why not, what instead.
	invalid, ok := errors.AsType[*config.InvalidError](err)
	if !ok {
		t.Fatalf("error is %T, want *config.InvalidError", err)
	}
	if invalid.Key != "mode" {
		t.Errorf("error names key %q, want %q", invalid.Key, "mode")
	}
	msg := err.Error()
	for _, want := range []string{path, "--yolo", "git clone"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q:\n%s", want, msg)
		}
	}
	// And it does not send the reader to a file that would be refused as well.
	if strings.Contains(msg, "global config instead") {
		t.Errorf("the refusal advises moving the value somewhere it is also refused:\n%s", msg)
	}
}

// TestProjectYoloIsRejectedEvenWhenOverridden: the file is still asking.
func TestProjectYoloIsRejectedEvenWhenOverridden(t *testing.T) {
	_, err := config.Load(config.Sources{
		GlobalPath: filepath.Join(t.TempDir(), config.File),
		ProjectDir: project(t, `{"mode": "yolo"}`),
		Getenv:     env{}.lookup,
		Flags:      map[string]string{"mode": "plan"},
	})
	if err == nil {
		t.Fatal("Load accepted a project config asking for yolo because a flag outranked it")
	}
}

// TestYoloIsNotSelectableFromAnyLayer. Every one of them, including the user's
// own global file: `mode` picks a preset the ladder runs under, and yolo is a
// bypass ahead of the ladder that is armed for one run. A layer that took the
// value would put it back on every run after it, which is what the bypass may
// never do — and the refusal has to name the way that does work, or a reader who
// meant it has nowhere to go.
func TestYoloIsNotSelectableFromAnyLayer(t *testing.T) {
	tests := []struct {
		name string
		src  config.Sources
	}{
		{name: "global config", src: config.Sources{GlobalPath: global(t, `{"mode": "yolo"}`)}},
		{name: "project config", src: config.Sources{ProjectDir: project(t, `{"mode": "yolo"}`)}},
		{name: "environment", src: config.Sources{Getenv: env{"RASP_MODE": "yolo"}.lookup}},
		{name: "flag", src: config.Sources{Flags: map[string]string{"mode": "yolo"}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.src
			if src.GlobalPath == "" {
				src.GlobalPath = filepath.Join(t.TempDir(), config.File)
			}
			if src.ProjectDir == "" {
				src.ProjectDir = t.TempDir()
			}
			if src.Getenv == nil {
				src.Getenv = env{}.lookup
			}

			_, err := config.Load(src)
			if err == nil {
				t.Fatalf("Load accepted yolo from the %s", tc.name)
			}
			if !strings.Contains(err.Error(), "--yolo") {
				t.Errorf("error does not point at --yolo:\n%s", err)
			}
		})
	}
}

// TestUnknownModeIsRejected: the mode stands between the agent and the
// filesystem, so a typo resolving to something reasonable is the worse outcome.
func TestUnknownModeIsRejected(t *testing.T) {
	_, err := config.Load(config.Sources{
		GlobalPath: global(t, `{"mode": "manaul"}`),
		ProjectDir: t.TempDir(),
		Getenv:     env{}.lookup,
	})
	if err == nil {
		t.Fatal(`Load accepted "mode": "manaul"`)
	}
	for _, want := range []string{"manaul", "plan", "manual", "auto"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%s", want, err)
		}
	}
}

// TestModeMustBeAString names what arrived, rather than failing later with a
// decoder message about Go types.
func TestModeMustBeAString(t *testing.T) {
	_, err := config.Load(config.Sources{
		GlobalPath: global(t, `{"mode": ["manual"]}`),
		ProjectDir: t.TempDir(),
		Getenv:     env{}.lookup,
	})
	if err == nil {
		t.Fatal("Load accepted a non-string mode")
	}
	if !strings.Contains(err.Error(), "array") {
		t.Errorf("error does not say what arrived instead:\n%s", err)
	}
}

// TestModesYoloWarnsAndIsDropped is design §10's second constraint: dropped
// rather than passed on, and said out loud rather than dropped in silence.
func TestModesYoloWarnsAndIsDropped(t *testing.T) {
	globalPath := global(t, `{
	  "modes": {
	    "yolo": {"bash": {"rm -rf*": "deny"}},
	    "manual": {"bash": {"go test*": "allow"}}
	  }
	}`)
	res := load(t, config.Sources{GlobalPath: globalPath})

	if _, ok := res.Config.Modes["yolo"]; ok {
		t.Error("modes.yolo survived into the resolved config")
	}
	if _, ok := res.Origins["modes.yolo.bash.rm -rf*"]; ok {
		t.Error("modes.yolo left an origin behind pointing at a value nobody can read")
	}
	if got := res.Config.Modes["manual"].Bash["go test*"]; got != "allow" {
		t.Errorf("modes.manual.bash[go test*] = %q, want the sibling mode untouched", got)
	}

	var found bool
	for _, w := range res.Warnings {
		if w.Key == "modes.yolo" {
			found = true
			if w.Origin.Detail != globalPath {
				t.Errorf("warning origin = %v, want it to name %s", w.Origin, globalPath)
			}
		}
	}
	if !found {
		t.Errorf("modes.yolo was dropped without a warning; got %v", res.Warnings)
	}
}

// TestAModeOverrideMergesPerPattern is the config half of design §7.2's presets:
// an override says only what it changes, and two files can each change something
// different about the same mode.
func TestAModeOverrideMergesPerPattern(t *testing.T) {
	res := load(t, config.Sources{
		GlobalPath: global(t, `{"modes": {"manual": {"write": "allow", "bash": {"go test*": "allow"}}}}`),
		ProjectDir: project(t, `{"modes": {"manual": {"bash": {"go build*": "allow"}}}}`),
	})

	manual := res.Config.Modes[config.ModeManual]
	if manual.Write != "allow" {
		t.Errorf("modes.manual.write = %q, want the global file's answer", manual.Write)
	}
	if got := manual.Bash["go test*"]; got != "allow" {
		t.Errorf("modes.manual.bash[go test*] = %q, want the global file's pattern to survive "+
			"the project file's", got)
	}
	if got := manual.Bash["go build*"]; got != "allow" {
		t.Errorf("modes.manual.bash[go build*] = %q, want the project file's pattern", got)
	}
	if manual.Edit != "" {
		t.Errorf("modes.manual.edit = %q, want an override to say nothing about what it "+
			"did not mention", manual.Edit)
	}
}

// TestAnUnreadablePermissionRuleIsRejected. permission.Compile turns one away
// too, but the mode is all it can name by then — so the check that knows which
// file wrote it has to be the one that runs first.
func TestAnUnreadablePermissionRuleIsRejected(t *testing.T) {
	globalPath := global(t, `{"modes": {"plan": {"bash": {"go test*": "alow"}}}}`)

	_, err := config.Load(config.Sources{
		GlobalPath: globalPath,
		ProjectDir: t.TempDir(),
		Getenv:     env{}.lookup,
	})
	if err == nil {
		t.Fatal(`Load accepted "alow" as a permission rule`)
	}

	invalid, ok := errors.AsType[*config.InvalidError](err)
	if !ok {
		t.Fatalf("error is %T, want *config.InvalidError", err)
	}
	if want := "modes.plan.bash.go test*"; invalid.Key != want {
		t.Errorf("error names key %q, want %q", invalid.Key, want)
	}
	for _, want := range []string{globalPath, "alow", "allow, ask, deny"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%s", want, err)
		}
	}
}

// TestEveryRuleInAModeOverrideIsChecked walks ModePermissions rather than naming
// its fields, so a field added to it without a line in validate.go fails here
// instead of loading clean and being refused later by a message that names no
// file.
func TestEveryRuleInAModeOverrideIsChecked(t *testing.T) {
	typ := reflect.TypeFor[config.ModePermissions]()
	if typ.NumField() == 0 {
		t.Fatal("ModePermissions has no fields, so this test checked nothing")
	}

	for field := range typ.Fields() {
		key, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		t.Run(key, func(t *testing.T) {
			var contents, wantKey string
			switch field.Type.Kind() {
			case reflect.String:
				contents = fmt.Sprintf(`{"modes": {"manual": {%q: "aslow"}}}`, key)
				wantKey = "modes.manual." + key
			case reflect.Map:
				contents = fmt.Sprintf(`{"modes": {"manual": {%q: {"ls*": "aslow"}}}}`, key)
				wantKey = "modes.manual." + key + ".ls*"
			default:
				t.Fatalf("%s is a %s, which this test cannot write into a config file — so it "+
					"is not checking that field", field.Name, field.Type.Kind())
			}

			_, err := config.Load(config.Sources{
				GlobalPath: global(t, contents),
				ProjectDir: t.TempDir(),
				Getenv:     env{}.lookup,
			})
			if err == nil {
				t.Fatalf("Load accepted an unreadable rule at %s", wantKey)
			}
			invalid, ok := errors.AsType[*config.InvalidError](err)
			if !ok {
				t.Fatalf("error is %T, want *config.InvalidError: %v", err, err)
			}
			if invalid.Key != wantKey {
				t.Errorf("error names key %q, want %q", invalid.Key, wantKey)
			}
		})
	}
}

// TestAnEmptyPatternIsRejected: it matches an empty command and nothing else, so
// it is always a `*` that lost its star.
func TestAnEmptyPatternIsRejected(t *testing.T) {
	_, err := config.Load(config.Sources{
		GlobalPath: global(t, `{"modes": {"auto": {"bash": {"": "deny"}}}}`),
		ProjectDir: t.TempDir(),
		Getenv:     env{}.lookup,
	})
	if err == nil {
		t.Fatal("Load accepted an empty bash pattern")
	}
	if !strings.Contains(err.Error(), `"*"`) {
		t.Errorf("error does not say what to write instead:\n%s", err)
	}
}

// TestABrokenRuleUnderModesYoloIsStillOnlyAWarning. The whole block is dropped,
// so refusing to start over a typo inside it would be refusing over something
// nothing reads.
func TestABrokenRuleUnderModesYoloIsStillOnlyAWarning(t *testing.T) {
	res := load(t, config.Sources{
		GlobalPath: global(t, `{"modes": {"yolo": {"bash": {"rm -rf*": "dney"}}}}`),
	})
	if _, ok := res.Config.Modes["yolo"]; ok {
		t.Error("modes.yolo survived into the resolved config")
	}
}
