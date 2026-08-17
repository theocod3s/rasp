package main

import (
	"os"
	"path/filepath"
	"testing"
)

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

	real := os.Stdout
	os.Stdout = file
	code := execute([]string{"config", "path"})
	os.Stdout = real

	if code != 0 {
		t.Fatalf("exit status %d", code)
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		t.Fatalf("the command printed nothing, so it never ran: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("no log file at RASP_LOG_FILE, so logging was never initialised: %v", err)
	}
}
