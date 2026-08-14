package config_test

import (
	"errors"
	"path/filepath"
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
	for _, want := range []string{path, "--yolo", "global config"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q:\n%s", want, msg)
		}
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

// TestYoloIsAcceptedFromTheGlobalConfig is the other half.
func TestYoloIsAcceptedFromTheGlobalConfig(t *testing.T) {
	res := load(t, config.Sources{GlobalPath: global(t, `{"mode": "yolo"}`)})
	if got := res.Config.Mode; got != "yolo" {
		t.Errorf("mode = %q, want yolo to be accepted from the global config", got)
	}
}

// TestYoloIsNotSelectableThroughTheChain covers the layers design §10 does not
// list.
func TestYoloIsNotSelectableThroughTheChain(t *testing.T) {
	tests := []struct {
		name string
		src  config.Sources
	}{
		{
			name: "environment",
			src:  config.Sources{Getenv: env{"RASP_MODE": "yolo"}.lookup},
		},
		{
			name: "flag",
			src:  config.Sources{Flags: map[string]string{"mode": "yolo"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.src
			src.GlobalPath = filepath.Join(t.TempDir(), config.File)
			src.ProjectDir = t.TempDir()
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
