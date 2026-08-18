//go:build !windows

// These tests run real commands and probe the survivors with POSIX signals, so
// they are Unix-only. design §14 cross-compiles the Windows targets from a linux
// runner and runs no tests on them either way.

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/tool"
)

func TestBashInterleavesStdoutAndStderr(t *testing.T) {
	res := callBash(t, t.Context(), BashInput{Command: "echo one; echo two >&2; echo three"})

	if res.IsError {
		t.Fatalf("a command that succeeded came back as an error: %q", res.Content)
	}
	if want := "one\ntwo\nthree\n"; res.Content != want {
		t.Errorf("bash returned %q, want %q: the two streams have to reach the model in the order they were written", res.Content, want)
	}
}

func TestBashNonZeroExitIsAResultNotAGoError(t *testing.T) {
	res := callBash(t, t.Context(), BashInput{Command: "echo before; echo boom >&2; exit 3"})

	if !res.IsError {
		t.Errorf("a command that exited 3 came back as a success: %q", res.Content)
	}
	for _, want := range []string{"before", "boom", "exit status 3"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("bash returned %q, which does not mention %q", res.Content, want)
		}
	}
	if code := bashDetails(t, res).ExitCode; code != 3 {
		t.Errorf("bash reported exit code %d, want 3", code)
	}
}

func TestBashSaysSoWhenACommandPrintsNothing(t *testing.T) {
	res := callBash(t, t.Context(), BashInput{Command: "true"})

	if res.IsError {
		t.Fatalf("a command that succeeded came back as an error: %q", res.Content)
	}
	if res.Content == "" {
		t.Error("bash returned empty content, which is a tool_result block with nothing in it")
	}
}

func TestBashRefusesAnEmptyCommand(t *testing.T) {
	res := callBash(t, t.Context(), BashInput{Command: "   \n"})

	if !res.IsError {
		t.Errorf("bash accepted a blank command: %q", res.Content)
	}
	if res.Details != nil {
		t.Errorf("bash reported details for a command it never ran: %#v", res.Details)
	}
}

func TestBashReportsAShellItCouldNotStart(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	res := callBash(t, t.Context(), BashInput{Command: "echo hello"})

	if !res.IsError {
		t.Errorf("a command that never started came back as a success: %q", res.Content)
	}
	if !strings.Contains(res.Content, "could not run") {
		t.Errorf("bash returned %q, which does not tell the model the shell itself is missing", res.Content)
	}
	if code := bashDetails(t, res).ExitCode; code != -1 {
		t.Errorf("bash reported exit code %d for a command that never ran, want -1", code)
	}
}

func TestBashTimeoutKillsTheCommand(t *testing.T) {
	started := time.Now()
	res := callBash(t, t.Context(), BashInput{Command: "sleep 30", TimeoutMS: 200})
	elapsed := time.Since(started)

	if !res.IsError {
		t.Errorf("a command killed at its timeout came back as a success: %q", res.Content)
	}
	if !strings.Contains(res.Content, "timeout") {
		t.Errorf("bash returned %q, which does not tell the model the command was killed at its timeout", res.Content)
	}
	if elapsed > 10*time.Second {
		t.Errorf("bash waited %s for a command it was told to kill after 200ms", elapsed)
	}
}

func TestBashTimeoutHasADefaultAndACeiling(t *testing.T) {
	cases := []struct {
		name string
		ms   int
		want time.Duration
	}{
		{"absent", 0, bashDefaultTimeout},
		{"negative", -1, bashDefaultTimeout},
		{"set by the call", 1500, 1500 * time.Millisecond},
		{"past the ceiling", 60 * 60 * 1000, bashMaxTimeout},
		{"large enough to overflow a Duration", math.MaxInt, bashMaxTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bashTimeout(c.ms); got != c.want {
				t.Errorf("bashTimeout(%d) = %s, want %s", c.ms, got, c.want)
			}
		})
	}
}

// The numbers in the timeout's description are prompt text and nothing derives
// them from the constants the tool applies, so they can drift into telling the
// model a limit that is not the one it will hit.
func TestBashSchemaNamesTheTimeoutItApplies(t *testing.T) {
	schema := Bash.Schema()

	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("the bash schema has no properties object: %v", schema)
	}
	field, ok := properties["timeout_ms"].(map[string]any)
	if !ok {
		t.Fatalf("the bash schema has no timeout_ms property: %v", properties)
	}
	desc, ok := field["description"].(string)
	if !ok || desc == "" {
		t.Fatalf("timeout_ms has no description for the model to read: %v", field)
	}

	for _, want := range []time.Duration{bashDefaultTimeout, bashMaxTimeout} {
		ms := strconv.FormatInt(want.Milliseconds(), 10)
		if !strings.Contains(desc, ms) {
			t.Errorf("the timeout description does not name %s as %s ms, so the model is told a limit rasp does not apply: %q", want, ms, desc)
		}
	}

	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("the bash schema does not list its required properties: %v", schema)
	}
	if !slices.Equal(required, []string{"command"}) {
		t.Errorf("bash requires %v; the timeout has to stay optional for a default to mean anything", required)
	}
}

// A command can exit while something it started still holds the output pipe, and
// waiting for end-of-file then means waiting for that process instead.
func TestBashDoesNotWaitForABackgroundProcessHoldingTheOutput(t *testing.T) {
	// The loop keeps the pipe open for 30s and ends on its own when rasp closes
	// the pipe under it, so nothing survives this test either way.
	const command = `for i in {1..600}; do printf .; sleep 0.05; done & echo started`

	started := time.Now()
	res := callBash(t, t.Context(), BashInput{Command: command})
	elapsed := time.Since(started)

	if res.IsError {
		t.Errorf("a command that exited 0 came back as an error: %q", res.Content)
	}
	if !strings.Contains(res.Content, "started") {
		t.Errorf("bash returned %q, which is missing the command's own output", res.Content)
	}
	if !strings.Contains(res.Content, "still held its output open") {
		t.Errorf("bash returned %q, which does not tell the model its output stops early", res.Content)
	}
	if elapsed > 15*time.Second {
		t.Errorf("bash waited %s on a process it did not start; WaitDelay bounds that at %s", elapsed, bashWaitDelay)
	}
}

// The one that matters: `sleep 300 &` outlives the bash that started it, so
// killing the command alone leaves it holding whatever it holds — a port, a
// lock, a database — for as long as the machine is up.
func TestBashCancelKillsTheWholeProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	// bash records its own pid, which is also the group id because it leads a
	// group of its own, then the pid of the grandchild that outlives it.
	command := fmt.Sprintf(`sleep 300 & printf '%%d\n%%d\n' "$$" "$!" > %q; wait`, pidFile)
	args := bashArgs(t, BashInput{Command: command})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	type outcome struct {
		res tool.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := Bash.Run(ctx, args)
		done <- outcome{res, err}
	}()

	group, child := waitForPIDs(t, pidFile)
	// Each pid separately as well as the group: when this test fails it is
	// because the group is not what the tool thinks it is, and a cleanup that
	// only signals the group would then leave behind the very orphan it caught.
	t.Cleanup(func() {
		for _, pid := range []int{-group, group, child} {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	// Without this the whole test passes on a probe that can no longer find
	// anything, which is the quietest way for it to stop proving its point.
	if !alive(child) || !alive(-group) {
		t.Fatalf("nothing here to kill before the cancel even happens: grandchild %d alive=%t, group %d alive=%t",
			child, alive(child), group, alive(-group))
	}

	cancel()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("bash returned a Go error, which is reserved for a tool that could not run at all: %v", got.err)
		}
		if !got.res.IsError {
			t.Errorf("an interrupted command came back as a success: %q", got.res.Content)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("bash never returned after its context was cancelled")
	}

	waitUntilGone(t, child, "the grandchild the command left running")
	waitUntilGone(t, -group, "the command's process group")
}

func callBash(t *testing.T, ctx context.Context, in BashInput) tool.Result {
	t.Helper()
	res, err := Bash.Run(ctx, bashArgs(t, in))
	if err != nil {
		t.Fatalf("bash returned a Go error, which is reserved for a tool that could not run at all: %v", err)
	}
	return res
}

func bashArgs(t *testing.T, in BashInput) json.RawMessage {
	t.Helper()
	args, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("encoding %#v as arguments: %v", in, err)
	}
	return args
}

func bashDetails(t *testing.T, res tool.Result) *BashDetails {
	t.Helper()
	details, ok := res.Details.(*BashDetails)
	if !ok {
		t.Fatalf("bash returned %T as its UI payload, want *BashDetails", res.Details)
	}
	return details
}

func waitForPIDs(t *testing.T, path string) (group, child int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if fields := strings.Fields(readAll(path)); len(fields) == 2 {
			return atoi(t, fields[0]), atoi(t, fields[1])
		}
		if time.Now().After(deadline) {
			t.Fatalf("the command never recorded its pids in %s, so there is nothing here to prove was killed", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitUntilGone(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			t.Errorf("%s (pid %d) survived the cancelled command", what, pid)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// alive asks whether signal 0 still finds a process, or — for a negative pid —
// any member of a process group. A pid nobody holds gives ESRCH; EPERM means it
// is there and belongs to somebody else.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func readAll(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

func atoi(t *testing.T, field string) int {
	t.Helper()
	n, err := strconv.Atoi(field)
	if err != nil {
		t.Fatalf("the command recorded %q where a pid belongs: %v", field, err)
	}
	return n
}
