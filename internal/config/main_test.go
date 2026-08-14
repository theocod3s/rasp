package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/goleak"

	"github.com/theocod3s/rasp/internal/config"
)

// TestMain runs the leak detector over the package. Nothing here starts a
// goroutine today; the shell expansion that resolves `$(op read …)` will, and
// a leak check added after the goroutines is a check that never saw them
// arrive.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// env is a stand-in for the process environment, so no test has to mutate the
// real one.
type env map[string]string

func (e env) lookup(key string) (string, bool) {
	val, ok := e[key]
	return val, ok
}

// project writes a project config under a fresh directory, ready for
// Sources.ProjectDir.
func project(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".rasp", config.File), contents)
	return dir
}

func global(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rasp", config.File)
	writeFile(t, path, contents)
	return path
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func load(t *testing.T, src config.Sources) *config.Result {
	t.Helper()
	// An unset GlobalPath would send the test at the developer's own config.
	if src.GlobalPath == "" {
		src.GlobalPath = filepath.Join(t.TempDir(), "absent", config.File)
	}
	if src.ProjectDir == "" {
		src.ProjectDir = t.TempDir()
	}
	if src.Getenv == nil {
		src.Getenv = env{}.lookup
	}

	res, err := config.Load(src)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return res
}
