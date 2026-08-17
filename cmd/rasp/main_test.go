package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs the leak detector over the package that assembles every other
// one. execute() is the whole startup path, and the goroutines the loop, the
// tools and the MCP servers spawn all end up under it (design §13).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// TestCommandsRunWithLoggingConfigured guards the wiring rather than the
// logging: logx claims the process's standard sinks when it opens the file, so
// a binary that never calls Init leaves every dependency writing to the
// terminal.
func TestCommandsRunWithLoggingConfigured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rasp.log")
	t.Setenv("RASP_LOG_FILE", path)
	// The data directory is where logging falls back to, and a test has no
	// business writing into the developer's real one.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	out := filepath.Join(t.TempDir(), "stdout")
	file, err := os.Create(out)
	if err != nil {
		t.Fatalf("creating %s: %v", out, err)
	}
	defer file.Close()

	code := func() int {
		real := os.Stdout
		os.Stdout = file
		defer func() { os.Stdout = real }()
		return execute([]string{"config", "path"})
	}()

	if code != 0 {
		t.Fatalf("exit status %d", code)
	}
	// The report `config path` prints, not merely some output: cobra falls back
	// to os.Args when handed no argument list, so a root command that ran
	// instead would also have printed something.
	printed, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading %s: %v", out, err)
	}
	if !strings.Contains(string(printed), "global") || !strings.Contains(string(printed), "project") {
		t.Fatalf("`config path` printed %q", printed)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("no log file at RASP_LOG_FILE, so logging was never initialised: %v", err)
	}
}
