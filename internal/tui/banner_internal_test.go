package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/theocod3s/rasp/internal/permission"
)

// TestBannerCarriesVersionAndModelStraightThrough is "one source, no second
// copy" at the point this package reads Config: the banner's Version and Model
// are cfg's own, untouched.
func TestBannerCarriesVersionAndModelStraightThrough(t *testing.T) {
	cfg := Config{Version: "v0.2.0", Model: "anthropic/claude-opus-5", Mode: permission.ModeAuto}

	b := banner(cfg)
	if b.Version != cfg.Version {
		t.Errorf("the banner carries version %q, cfg named %q", b.Version, cfg.Version)
	}
	if b.Model != cfg.Model {
		t.Errorf("the banner carries model %q, cfg named %q", b.Model, cfg.Model)
	}
	if b.Mode != string(cfg.Mode) {
		t.Errorf("the banner carries mode %q, cfg named %q", b.Mode, cfg.Mode)
	}
}

// TestBannerDefaultsAnUnnamedModeToManual matches status.go's own default: a
// Config nothing set a mode on is a session in manual, not a banner that names
// no mode at all.
func TestBannerDefaultsAnUnnamedModeToManual(t *testing.T) {
	if got := banner(Config{}).Mode; got != string(permission.ModeManual) {
		t.Errorf("an unset mode reads %q on the banner, want %q", got, permission.ModeManual)
	}
}

// TestBannerAbbreviatesTheHomeDirectory is the banner's cwd row as the ticket
// asks for it: the resolved root, ~-abbreviated.
func TestBannerAbbreviatesTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	for _, tc := range []struct {
		name string
		cwd  string
		want string
	}{
		{"the home directory itself", home, "~"},
		{"a directory under it", filepath.Join(home, "scratch", "rasp-demo"), filepath.Join("~", "scratch", "rasp-demo")},
		{"a directory outside it", string(filepath.Separator) + filepath.Join("srv", "rasp-demo"), string(filepath.Separator) + filepath.Join("srv", "rasp-demo")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := banner(Config{Cwd: tc.cwd}).Cwd; got != tc.want {
				t.Errorf("cwd %q abbreviates to %q, want %q", tc.cwd, got, tc.want)
			}
		})
	}
}

// TestBannerLeavesCwdAloneWithNoHomeToAbbreviateAgainst. A home directory Go
// itself could not resolve is not a reason to show nothing in the row a real
// session's tools are actually confined to.
func TestBannerLeavesCwdAloneWithNoHomeToAbbreviateAgainst(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	const cwd = "/srv/rasp-demo"
	if got := banner(Config{Cwd: cwd}).Cwd; got != cwd {
		t.Errorf("cwd reads %q with no home directory to abbreviate against, want it unabbreviated: %q", got, cwd)
	}
}

// TestBannerReadsNoColorFromTheEnvironment is read once, at construction, so a
// session launched under NO_COLOR gets the plain word rather than the art —
// the fallback chat.Banner itself draws when told NoColor (banner_test.go).
// The convention is presence, not value, so an empty NO_COLOR counts too.
func TestBannerReadsNoColorFromTheEnvironment(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"a non-empty value", "1"},
		{"present but empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.value)
			if !banner(Config{}).NoColor {
				t.Error("NO_COLOR is set and the banner reads NoColor = false")
			}
		})
	}
}

// TestBannerIgnoresAnUnsetNoColor is the negative control: NO_COLOR must be
// unset for the test above to mean anything, and this pins the assumption
// rather than leaving it implicit. The prior value, if any, is restored rather
// than left unset for the remainder of the test binary — this package's other
// tests should see the environment the way the developer or CI runner set it.
func TestBannerIgnoresAnUnsetNoColor(t *testing.T) {
	if prev, ok := os.LookupEnv("NO_COLOR"); ok {
		t.Cleanup(func() { os.Setenv("NO_COLOR", prev) })
	}
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatalf("unsetting NO_COLOR: %v", err)
	}
	if banner(Config{}).NoColor {
		t.Error("NO_COLOR is unset and the banner reads NoColor = true")
	}
}
