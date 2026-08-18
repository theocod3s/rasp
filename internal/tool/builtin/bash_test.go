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

// A 2MB log costs more context than every other message in the turn put
// together, and the two ends are where the diagnosis is: the command echo at the
// top, the error at the bottom (internals §3.5).
func TestBashKeepsBothEndsOfOutputTooLongToReturn(t *testing.T) {
	// ~109KB, more than twice the cap, with a line the middle must swallow.
	res := callBash(t, t.Context(), BashInput{Command: "echo HEAD-MARKER; seq 1 20000; echo TAIL-MARKER"})

	if res.IsError {
		t.Fatalf("a command that succeeded came back as an error: %s", excerpt(res.Content))
	}
	if len(res.Content) > bashMaxOutput {
		t.Errorf("bash returned %d bytes, past its own %d-byte cap", len(res.Content), bashMaxOutput)
	}

	head, dropped, tail, note := splitAtOmission(t, res.Content)
	if !strings.HasPrefix(head, "HEAD-MARKER\n") {
		t.Errorf("what bash kept from the start does not begin with the command's first line: %s", excerpt(head))
	}
	if !strings.HasSuffix(tail, "TAIL-MARKER") {
		t.Errorf("what bash kept from the end does not reach the command's last line: %s", excerpt(tail))
	}
	if strings.Contains(res.Content, "\n10000\n") {
		t.Error("bash returned a line from the middle of the output, so nothing was actually dropped there")
	}
	if dropped <= 0 {
		t.Errorf("bash marked the output truncated but says %d bytes went", dropped)
	}

	details := bashDetails(t, res)
	if !details.Truncated {
		t.Error("bash dropped the middle of the output without recording it in the details the UI draws from")
	}
	if details.SpillPath == "" {
		t.Fatal("bash truncated the output and saved none of it")
	}
	if !strings.Contains(note, details.SpillPath) {
		t.Errorf("the closing note does not name the file holding the rest of the output: %q", note)
	}

	full := readSpill(t, details.SpillPath)
	if len(full) <= bashMaxOutput {
		t.Errorf("the saved output is %d bytes, which would have fit in the %d-byte cap unaltered", len(full), bashMaxOutput)
	}
	if !strings.HasPrefix(full, head) {
		t.Error("the head bash returned is not how the saved output starts")
	}
	if !strings.HasSuffix(strings.TrimRight(full, "\n"), tail) {
		t.Error("the tail bash returned is not how the saved output ends")
	}
	if !strings.Contains(full, "\n10000\n") {
		t.Error("the saved output is missing the middle, which is the only reason to save it")
	}
}

func TestBashDoesNotSaveOutputThatFits(t *testing.T) {
	res := callBash(t, t.Context(), BashInput{Command: "echo small"})

	details := bashDetails(t, res)
	if details.Truncated || details.SpillPath != "" {
		t.Errorf("bash truncated %q and saved it to %q; it is six bytes long", res.Content, details.SpillPath)
	}
}

// The cap covers the closing note as well as the marker, so a command that both
// floods and fails cannot come back over it.
func TestBashOutputCapCoversEverythingAppendedToIt(t *testing.T) {
	cases := []struct {
		name          string
		size          int
		note          string
		wantTruncated bool
	}{
		{"exactly the cap, nothing appended", bashMaxOutput, "", false},
		{"one byte past the cap", bashMaxOutput + 1, "", true},
		{"room for the note", bashMaxOutput - 64, "exit status 3", false},
		{"under the cap until the note is added", bashMaxOutput - 4, "exit status 3", true},
		{"far past the cap", 4 * bashMaxOutput, "exit status 3", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			output := strings.Repeat("x", c.size)

			content, truncated, spill := bashOutput(output, c.note)
			if spill != "" {
				t.Cleanup(func() { _ = os.Remove(spill) })
			}

			if len(content) > bashMaxOutput {
				t.Errorf("bash returned %d bytes for a %d-byte output and a %d-byte note, past its own %d-byte cap",
					len(content), c.size, len(c.note), bashMaxOutput)
			}
			if truncated != c.wantTruncated {
				t.Errorf("bash reports truncated=%t for a %d-byte output and a %d-byte note, want %t",
					truncated, c.size, len(c.note), c.wantTruncated)
			}
			if c.note != "" && !strings.Contains(content, c.note) {
				t.Errorf("bounding the output dropped the note %q that goes after it", c.note)
			}
			if !c.wantTruncated {
				if spill != "" {
					t.Errorf("bash saved an output it returned whole, to %s", spill)
				}
				return
			}
			if spill == "" {
				t.Fatal("bash truncated the output and saved none of it")
			}
			if got := readSpill(t, spill); got != output {
				t.Errorf("the saved output is %d bytes, want the %d that were produced", len(got), len(output))
			}
		})
	}
}

// The property the marker itself threatens: it is inserted to report an overflow
// and is capable of causing one. Exhaustive over every limit around the two
// interesting sizes — the marker's own length, and the input's.
func TestHeadAndTailNeverExceedsItsLimit(t *testing.T) {
	const input = 2000
	s := strings.Repeat("abcdefghij", input/10)

	markers := map[string]func(int) string{
		"the one bash uses":             bashOmitted,
		"one that grows with the count": func(dropped int) string { return fmt.Sprintf("<%d>", dropped) },
		"one longer than small limits":  func(dropped int) string { return strings.Repeat("x", 300) + strconv.Itoa(dropped) },
	}
	for name, marker := range markers {
		t.Run(name, func(t *testing.T) {
			for limit := -4; limit <= len(s)+16; limit++ {
				got := headAndTail(s, limit, marker)
				if limit >= 0 && len(got) > limit {
					t.Fatalf("bounding %d bytes to %d returned %d of them", len(s), limit, len(got))
				}
				if limit >= len(s) && got != s {
					t.Fatalf("bounding %d bytes to %d altered them; nothing had to go", len(s), limit)
				}
			}
		})
	}
}

func TestHeadAndTailKeepsBothEndsAndSaysWhatWent(t *testing.T) {
	s := strings.Repeat("abcdefghij", 200)
	marker := func(dropped int) string { return fmt.Sprintf("[%d]", dropped) }

	for _, limit := range []int{64, 100, 512, 1000, 1999} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			got := headAndTail(s, limit, marker)

			head, after, found := strings.Cut(got, "[")
			if !found {
				t.Fatalf("bounding %d bytes to %d dropped %d of them without saying so", len(s), limit, len(s)-len(got))
			}
			count, tail, _ := strings.Cut(after, "]")
			dropped := atoi(t, count)

			if head == "" || tail == "" {
				t.Errorf("bounding to %d kept %d bytes of head and %d of tail; both ends have to survive", limit, len(head), len(tail))
			}
			if !strings.HasPrefix(s, head) {
				t.Errorf("what came back as the head is not how the input starts: %s", excerpt(head))
			}
			if !strings.HasSuffix(s, tail) {
				t.Errorf("what came back as the tail is not how the input ends: %s", excerpt(tail))
			}
			if want := len(s) - len(head) - len(tail); dropped != want {
				t.Errorf("the marker says %d bytes went, but %d of the %d shown up as head or tail", dropped, want, len(s))
			}
		})
	}
}

// splitAtOmission takes bash's content apart at the marker: what it kept from
// the start, the count the marker reports, what it kept from the end, and the
// note appended after all of it.
func splitAtOmission(t *testing.T, content string) (head string, dropped int, tail, note string) {
	t.Helper()

	head, after, found := strings.Cut(content, "\n\n[")
	if !found {
		t.Fatalf("bash returned %d bytes with no omission marker among them: %s", len(content), excerpt(content))
	}
	count, rest, found := strings.Cut(after, " bytes omitted from the middle of the output]\n\n")
	if !found {
		t.Fatalf("bash's omission marker does not read as one: %s", excerpt("["+after))
	}
	tail, note, found = strings.Cut(rest, "\n\n")
	if !found {
		t.Fatalf("bash truncated the output and appended no note saying where the rest went: %s", excerpt(rest))
	}
	return head, atoi(t, count), tail, note
}

func readSpill(t *testing.T, path string) string {
	t.Helper()
	t.Cleanup(func() { _ = os.Remove(path) })

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the output bash saved to %s: %v", path, err)
	}
	return string(data)
}

// excerpt keeps a failure message readable when its subject is 50KB of output.
func excerpt(s string) string {
	const keep = 120
	if len(s) <= keep {
		return strconv.Quote(s)
	}
	return strconv.Quote(s[:keep]) + fmt.Sprintf(" (%d bytes in all)", len(s))
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
