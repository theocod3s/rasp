package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/config"
)

// theDefaultCap is design §10's documented number, written out rather than read
// back from Defaults, which would agree with whatever it found.
const theDefaultCap = 16384

// TestTheOutputCapResolvesThroughTheChain: reaching the cap fails a run, so the
// number has to be one a user can raise, from the layers a user writes in.
func TestTheOutputCapResolvesThroughTheChain(t *testing.T) {
	globalPath := global(t, `{"max_output_tokens": 32768}`)
	projectDir := project(t, `{"max_output_tokens": 65536}`)
	projectPath := filepath.Join(projectDir, ".rasp", config.File)

	tests := []struct {
		name   string
		src    config.Sources
		want   int
		origin config.Origin
	}{
		{
			name:   "the built-in default",
			src:    config.Sources{},
			want:   theDefaultCap,
			origin: config.Origin{Layer: config.LayerDefault},
		},
		{
			name:   "the global file beats it",
			src:    config.Sources{GlobalPath: globalPath},
			want:   32768,
			origin: config.Origin{Layer: config.LayerGlobal, Detail: globalPath},
		},
		{
			name:   "the project file beats the global one",
			src:    config.Sources{GlobalPath: globalPath, ProjectDir: projectDir},
			want:   65536,
			origin: config.Origin{Layer: config.LayerProject, Detail: projectPath},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := load(t, tc.src)
			if got := res.Config.MaxOutputTokens; got != tc.want {
				t.Errorf("max_output_tokens = %d, want %d", got, tc.want)
			}
			if got := res.Origins["max_output_tokens"]; got != tc.origin {
				t.Errorf("origin = %+v, want %+v", got, tc.origin)
			}
		})
	}
}

// TestACapAboveWhatAModelTakesIsKept. No model id is resolved against a catalog,
// so a ceiling is not ours to enforce: the request goes out as written and the
// API refuses it, the same answer an effort rung a model lacks gets.
func TestACapAboveWhatAModelTakesIsKept(t *testing.T) {
	res := load(t, config.Sources{ProjectDir: project(t, `{"max_output_tokens": 1000000}`)})

	if got := res.Config.MaxOutputTokens; got != 1000000 {
		t.Errorf("max_output_tokens = %d, want the configured value kept as written", got)
	}
	if len(res.Warnings) > 0 {
		t.Errorf("warnings = %v, want a large cap accepted in silence", res.Warnings)
	}
}

// TestACapWithNoRoomForAReplyIsRefused, naming the file it came from — the
// question a refusal has to answer is which of four sources said that.
func TestACapWithNoRoomForAReplyIsRefused(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{name: "zero", file: `{"max_output_tokens": 0}`},
		{name: "negative", file: `{"max_output_tokens": -1}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			projectDir := project(t, tc.file)
			_, err := config.Load(config.Sources{
				GlobalPath: filepath.Join(t.TempDir(), "absent", config.File),
				ProjectDir: projectDir,
				Getenv:     env{}.lookup,
			})
			if err == nil {
				t.Fatalf("Load accepted %s", tc.file)
			}
			for _, want := range []string{
				"max_output_tokens",
				filepath.Join(projectDir, ".rasp", config.File),
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// TestAnOverriddenCapIsNotRefusedOnBehalfOfARunThatNeverSeesIt: the floor is
// about the resolved value, unlike the rules about `mode`, which are about the
// layer that wrote one.
func TestAnOverriddenCapIsNotRefusedOnBehalfOfARunThatNeverSeesIt(t *testing.T) {
	res := load(t, config.Sources{
		GlobalPath: global(t, `{"max_output_tokens": 0}`),
		ProjectDir: project(t, `{"max_output_tokens": 8192}`),
	})

	if got := res.Config.MaxOutputTokens; got != 8192 {
		t.Errorf("max_output_tokens = %d, want the project value", got)
	}
}

// TestTheOutputCapIsNotTheCompactionReserve. §11 makes the reserve
// `max(maxOutput, 4096) + 12_000` — derived from the cap and larger than it — and
// the two defaults being the same number today is exactly what would hide a
// wiring that read one for the other. So each is set alone and the other is
// checked to have stayed where it was.
func TestTheOutputCapIsNotTheCompactionReserve(t *testing.T) {
	capOnly := load(t, config.Sources{ProjectDir: project(t, `{"max_output_tokens": 4096}`)})
	if got := capOnly.Config.MaxOutputTokens; got != 4096 {
		t.Errorf("max_output_tokens = %d, want 4096", got)
	}
	if got := capOnly.Config.Context.ReserveTokens; got != theDefaultCap {
		t.Errorf("context.reserve_tokens = %d, want the default %d — the cap moved it",
			got, theDefaultCap)
	}

	reserveOnly := load(t, config.Sources{
		ProjectDir: project(t, `{"context": {"reserve_tokens": 40000}}`),
	})
	if got := reserveOnly.Config.Context.ReserveTokens; got != 40000 {
		t.Errorf("context.reserve_tokens = %d, want 40000", got)
	}
	if got := reserveOnly.Config.MaxOutputTokens; got != theDefaultCap {
		t.Errorf("max_output_tokens = %d, want the default %d — the reserve moved it",
			got, theDefaultCap)
	}
}
