package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// run executes the command tree with args and returns what it wrote.
func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, errOut bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)

	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// projectConfig writes a project config into a temp directory and makes it the
// working directory, with an empty global config alongside — so a test never
// reads whatever the developer running it happens to have configured.
func projectConfig(t *testing.T, contents string) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, ".rasp", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(dir)
}

// TestConfigCheckNamesAnOriginForEveryValue is the acceptance criterion read
// as the user meets it: every line of the report says where that value came
// from.
func TestConfigCheckNamesAnOriginForEveryValue(t *testing.T) {
	projectConfig(t, `{"model": "a/b"}`)

	stdout, _, err := run(t, "config", "check")
	if err != nil {
		t.Fatalf("config check: %v", err)
	}

	_, settings, ok := strings.Cut(stdout, "settings\n")
	if !ok {
		t.Fatalf("no settings section in:\n%s", stdout)
	}

	var lines int
	for line := range strings.SplitSeq(strings.TrimSpace(settings), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		lines++
		// The origin is the tail of the line: "built-in default", "project
		// <path>", "env RASP_MODEL", "flag --mode".
		if !hasOrigin(line) {
			t.Errorf("no origin on settings line: %q", line)
		}
	}
	if lines == 0 {
		t.Fatalf("the settings section is empty:\n%s", stdout)
	}
}

func hasOrigin(line string) bool {
	for _, marker := range []string{"built-in default", "global ", "project ", "env ", "flag --"} {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

// TestConfigCheckHidesCredentials. The report is meant to be pasted into an
// issue; an API key printed alongside its origin would make that a leak.
func TestConfigCheckHidesCredentials(t *testing.T) {
	const key = "sk-ant-not-a-real-key"
	projectConfig(t, `{"providers": {"anthropic": {"api_key": "`+key+`"}}}`)

	stdout, stderr, err := run(t, "config", "check")
	if err != nil {
		t.Fatalf("config check: %v", err)
	}
	if strings.Contains(stdout+stderr, key) {
		t.Errorf("the report printed the API key:\n%s", stdout)
	}
	if !strings.Contains(stdout, "providers.anthropic.api_key") {
		t.Errorf("the report omitted the key's origin entirely:\n%s", stdout)
	}
}

// TestConfigCheckFailsOnAProjectYolo: the rejection has to reach the exit
// status, not just the resolver.
func TestConfigCheckFailsOnAProjectYolo(t *testing.T) {
	projectConfig(t, `{"mode": "yolo"}`)

	if _, _, err := run(t, "config", "check"); err == nil {
		t.Fatal("config check succeeded on a project config asking for yolo")
	}
}

// TestOnlyChangedFlagsReachTheChain. A flag sitting at its default would
// otherwise outrank every file in the chain while carrying no instruction.
func TestOnlyChangedFlagsReachTheChain(t *testing.T) {
	cmd := newRootCmd()
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("parsing no flags: %v", err)
	}
	if got := changedFlags(cmd); len(got) != 0 {
		t.Errorf("changedFlags with nothing set = %v, want empty", got)
	}

	cmd = newRootCmd()
	if err := cmd.ParseFlags([]string{"--mode", "plan"}); err != nil {
		t.Fatalf("parsing --mode: %v", err)
	}
	got := changedFlags(cmd)
	if len(got) != 1 || got["mode"] != "plan" {
		t.Errorf("changedFlags with --mode plan = %v, want {mode: plan}", got)
	}
}

// TestAFlagOnASubcommandReachesTheChain. The flags are persistent on the root,
// and `rasp config check --mode plan` is where that has to hold: a flag the
// report cannot see is a flag with no effect on what the report says.
func TestAFlagOnASubcommandReachesTheChain(t *testing.T) {
	projectConfig(t, `{"mode": "manual"}`)

	stdout, _, err := run(t, "config", "check", "--mode", "plan")
	if err != nil {
		t.Fatalf("config check --mode plan: %v", err)
	}
	if !strings.Contains(stdout, "flag --mode") {
		t.Errorf("the report does not attribute mode to the flag:\n%s", stdout)
	}
}

func TestConfigPathNamesBothFiles(t *testing.T) {
	projectConfig(t, `{}`)

	stdout, _, err := run(t, "config", "path")
	if err != nil {
		t.Fatalf("config path: %v", err)
	}
	for _, want := range []string{"global", "project", filepath.Join(".rasp", "config.json")} {
		if !strings.Contains(stdout, want) {
			t.Errorf("config path output does not mention %q:\n%s", want, stdout)
		}
	}
}
