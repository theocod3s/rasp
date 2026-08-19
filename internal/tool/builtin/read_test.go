package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tool/builtin"
	"github.com/theocod3s/rasp/internal/workspace"
)

// harness is a workspace with the read tool built over it, plus the tracker that
// tool records into, so a test can assert on both halves of one call.
type harness struct {
	dir     string // the workspace root as passed to Open, symlinks unresolved
	outside string // a directory beside it, off limits
	ws      *workspace.Workspace
	reads   *workspace.Tracker
	subject tool.Tool
}

func newHarness(t *testing.T) harness {
	t.Helper()

	base := t.TempDir()
	dir := filepath.Join(base, "ws")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{dir, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("creating %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("writing the file outside the workspace: %v", err)
	}

	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatalf("opening the workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := ws.Close(); err != nil {
			t.Errorf("closing the workspace: %v", err)
		}
	})

	reads := workspace.NewTracker()
	return harness{dir: dir, outside: outside, ws: ws, reads: reads, subject: builtin.NewRead(ws, reads)}
}

func (h harness) write(t *testing.T, name, content string) {
	t.Helper()

	path := filepath.Join(h.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating the parent of %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// call runs the tool the way the loop does, through the JSON the model sends. A
// Go error is fatal: everything these tests are about comes back as a Result.
func (h harness) call(t *testing.T, args map[string]any) tool.Result {
	t.Helper()

	res, err := h.run(t, context.Background(), args)
	if err != nil {
		t.Fatalf("read(%v) returned the Go error %v; a file it cannot read is a failed result, not a "+
			"failed tool (design §3.4)", args, err)
	}
	return res
}

func (h harness) run(t *testing.T, ctx context.Context, args map[string]any) (tool.Result, error) {
	t.Helper()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encoding the arguments %v: %v", args, err)
	}
	return h.subject.Run(ctx, raw)
}

// numbered is the rendering the model is promised: every line prefixed with its
// number, right-aligned to the widest one, and a tab. Built here from the line
// numbers a test names rather than from the tool, so a change to either side has
// to be a change to both.
func numbered(first int, lines ...string) string {
	width := len(strconv.Itoa(first + len(lines) - 1))
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%*d\t%s\n", width, first+i, line)
	}
	return b.String()
}

// lines builds a file whose every line names its own number, so an off-by-one in
// the window shows up as content rather than only as a count.
func lines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func TestReadReturnsEveryLineWithItsNumber(t *testing.T) {
	h := newHarness(t)
	h.write(t, "main.go", "package main\n\nfunc main() {}\n")

	res := h.call(t, map[string]any{"path": "main.go"})
	assertOK(t, res)

	want := numbered(1, "package main", "", "func main() {}")
	if res.Content != want {
		t.Errorf("read main.go returned\n%q\nwant\n%q", res.Content, want)
	}
}

// TestTheLineNumbersAreThisToolsAndNotTheFilesContent guards the promise the
// description makes to the model — strip the prefixes and what is left is the
// file, byte for byte. Comparing against the bytes on disk rather than against a
// literal is what makes it catch a prefix that leaks into the content.
func TestTheLineNumbersAreThisToolsAndNotTheFilesContent(t *testing.T) {
	h := newHarness(t)
	const content = "alpha\n\tindented\n\nlast line, no newline"
	h.write(t, "notes.txt", content)

	res := h.call(t, map[string]any{"path": "notes.txt"})
	assertOK(t, res)

	got := strings.Split(strings.TrimSuffix(res.Content, "\n"), "\n")
	if len(got) != 4 {
		t.Fatalf("read notes.txt returned %d lines, want the file's 4: %q", len(got), res.Content)
	}
	var stripped []string
	for i, line := range got {
		prefix := fmt.Sprintf("%d\t", i+1)
		if !strings.HasPrefix(line, prefix) {
			t.Fatalf("line %d is %q, which does not start with the prefix %q", i+1, line, prefix)
		}
		stripped = append(stripped, strings.TrimPrefix(line, prefix))
	}
	if joined := strings.Join(stripped, "\n"); joined != content {
		t.Errorf("stripping the prefixes gives %q, want the file's own bytes %q", joined, content)
	}
}

func TestReadTakesTheLineWindowItIsAsked(t *testing.T) {
	h := newHarness(t)
	h.write(t, "long.txt", lines(200))

	cases := []struct {
		what  string
		args  map[string]any
		first int
		last  int
	}{
		{"an offset and a limit", map[string]any{"path": "long.txt", "offset": 50, "limit": 10}, 50, 59},
		{"an offset alone runs to the end", map[string]any{"path": "long.txt", "offset": 196}, 196, 200},
		{"a limit alone starts at the top", map[string]any{"path": "long.txt", "limit": 3}, 1, 3},
		{"offset 1 is the top", map[string]any{"path": "long.txt", "offset": 1, "limit": 2}, 1, 2},
		{"a limit past the end stops at the end", map[string]any{"path": "long.txt", "offset": 199, "limit": 500}, 199, 200},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			res := h.call(t, c.args)
			assertOK(t, res)

			var want []string
			for n := c.first; n <= c.last; n++ {
				want = append(want, fmt.Sprintf("line %d", n))
			}
			if got := numbered(c.first, want...); res.Content != got {
				t.Errorf("read %v returned\n%q\nwant\n%q", c.args, res.Content, got)
			}
		})
	}
}

// TestTheNumberColumnIsWideEnoughForTheLastLine pins the alignment on a file
// whose line numbers change width, which is where a fixed column silently starts
// shifting the code one character left.
func TestTheNumberColumnIsWideEnoughForTheLastLine(t *testing.T) {
	h := newHarness(t)
	h.write(t, "twelve.txt", lines(12))

	res := h.call(t, map[string]any{"path": "twelve.txt"})
	assertOK(t, res)

	first, _, _ := strings.Cut(res.Content, "\n")
	if want := " 1\tline 1"; first != want {
		t.Errorf("the first line of a 12-line file is %q, want %q", first, want)
	}
}

// TestALineUnderTheCapIsNotRefused sits between the two limits that decide
// whether a long line comes back: the read cap, and the buffer the scanner is
// allowed to grow to. Left at its default the second is 64 KiB, and a line under
// the cap is refused for being over a limit nothing published.
func TestALineUnderTheCapIsNotRefused(t *testing.T) {
	h := newHarness(t)
	const width = 100 << 10
	h.write(t, "wide.txt", strings.Repeat("z", width)+"\n")

	res := h.call(t, map[string]any{"path": "wide.txt"})
	assertOK(t, res)
	if got := len(res.Content); got < width {
		t.Errorf("the line came back as %d bytes, want at least the %d it is on disk", got, width)
	}
}

func TestReadPastTheEndOfTheFileSaysHowLongItIs(t *testing.T) {
	h := newHarness(t)
	h.write(t, "short.txt", lines(3))

	res := h.call(t, map[string]any{"path": "short.txt", "offset": 40})
	assertFailed(t, res, "offset 40", "3 lines")
}

func TestReadRefusesANegativeWindow(t *testing.T) {
	h := newHarness(t)
	h.write(t, "short.txt", lines(3))

	for _, args := range []map[string]any{
		{"path": "short.txt", "offset": -1},
		{"path": "short.txt", "limit": -5},
	} {
		res := h.call(t, args)
		assertFailed(t, res, "neither can be negative")
	}
}

func TestAFileThatIsNotThereIsAFailedResult(t *testing.T) {
	h := newHarness(t)

	res := h.call(t, map[string]any{"path": "nowhere.go"})
	assertFailed(t, res, "nowhere.go")
}

func TestReadingOutsideTheWorkspaceIsRefused(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"../outside/secret.txt", filepath.Join(h.outside, "secret.txt")} {
		res := h.call(t, map[string]any{"path": path})
		assertFailed(t, res, "outside the workspace")
		if strings.Contains(res.Content, "secret") && !strings.Contains(res.Content, "secret.txt") {
			t.Errorf("the refusal for %s leaked the file's contents: %q", path, res.Content)
		}
	}
}

func TestReadingADirectorySaysToUseLs(t *testing.T) {
	h := newHarness(t)
	h.write(t, "sub/file.txt", "x\n")

	res := h.call(t, map[string]any{"path": "sub"})
	assertFailed(t, res, "directory", "ls")
}

func TestAnEmptyFileIsNotAFailure(t *testing.T) {
	h := newHarness(t)
	h.write(t, "empty.txt", "")

	res := h.call(t, map[string]any{"path": "empty.txt"})
	assertOK(t, res)
	if res.Content == "" {
		t.Error("read of an empty file returned empty content, which providers refuse as a tool result")
	}
	if _, ok := h.reads.LastRead("empty.txt"); !ok {
		t.Error("read of an empty file recorded nothing; the model has still seen the file")
	}
}

// TestAFileOverTheCapIsRefusedWithAWindowThatWorks is the size cap and its
// message in one: the refusal has to name a window, and that window has to be one
// the tool then serves. Parsing the numbers back out is deliberate — a reworded
// message that stopped naming a window would pass an assertion on the word
// "offset" alone.
func TestAFileOverTheCapIsRefusedWithAWindowThatWorks(t *testing.T) {
	h := newHarness(t)

	const wide = 4000
	var b strings.Builder
	for i := 1; i <= wide; i++ {
		fmt.Fprintf(&b, "%s %d\n", strings.Repeat("x", 60), i)
	}
	h.write(t, "huge.txt", b.String())

	res := h.call(t, map[string]any{"path": "huge.txt"})
	assertFailed(t, res, "offset", "limit")

	window := regexp.MustCompile(`lines (\d+) to (\d+) fit`).FindStringSubmatch(res.Content)
	if window == nil {
		t.Fatalf("the refusal names no window a second call could use: %q", res.Content)
	}
	first, last := atoi(t, window[1]), atoi(t, window[2])
	if first != 1 || last <= 1 || last >= wide {
		t.Fatalf("the refusal offers lines %d to %d of a %d-line file, which is not a window inside it: %q",
			first, last, wide, res.Content)
	}

	retry := h.call(t, map[string]any{"path": "huge.txt", "offset": first, "limit": last - first + 1})
	assertOK(t, retry)
	if got := strings.Count(retry.Content, "\n"); got != last-first+1 {
		t.Errorf("the window the refusal named returned %d lines, want %d", got, last-first+1)
	}
}

// TestOneLineOverTheCapDoesNotPointAtAWindow: the windowed advice is wrong when a
// single line is the thing that does not fit, and sending the model round a loop
// of narrowing windows that can never work is worse than saying so.
func TestOneLineOverTheCapDoesNotPointAtAWindow(t *testing.T) {
	h := newHarness(t)
	h.write(t, "minified.js", strings.Repeat("y", 1<<20)+"\n")

	res := h.call(t, map[string]any{"path": "minified.js"})
	assertFailed(t, res, "line 1")
	if strings.Contains(res.Content, "lines 1 to") {
		t.Errorf("the refusal offers a window for a file whose first line is itself over the cap: %q", res.Content)
	}
	if _, ok := h.reads.LastRead("minified.js"); ok {
		t.Error("a refused read was recorded; nothing was returned for the model to have seen")
	}
}

func TestASuccessfulReadIsRecordedWithTheMtimeItSaw(t *testing.T) {
	h := newHarness(t)
	h.write(t, "sub/main.go", lines(4))

	if _, ok := h.reads.LastRead("sub/main.go"); ok {
		t.Fatal("the tracker reports a read before anything read; every assertion below would pass " +
			"against a lookup that answers regardless")
	}

	res := h.call(t, map[string]any{"path": "sub/main.go"})
	assertOK(t, res)

	got, ok := h.reads.LastRead("sub/main.go")
	if !ok {
		t.Fatal("a successful read recorded nothing, so a later edit has no way to know the model saw the file")
	}
	info, err := os.Stat(filepath.Join(h.dir, "sub", "main.go"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !got.Equal(info.ModTime()) {
		t.Errorf("the recorded mtime is %v, want the file's own %v", got, info.ModTime())
	}
}

// TestEverySpellingOfOnePathRecordsTheSameRead: the key has to be what Resolve
// returns, or a file read as ./main.go and edited as main.go looks unread.
func TestEverySpellingOfOnePathRecordsTheSameRead(t *testing.T) {
	h := newHarness(t)
	h.write(t, "sub/main.go", lines(2))

	for _, spelling := range []string{
		"./sub/main.go",
		"sub/../sub/main.go",
		filepath.Join(h.dir, "sub", "main.go"),
	} {
		t.Run(spelling, func(t *testing.T) {
			reads := workspace.NewTracker()
			subject := builtin.NewRead(h.ws, reads)
			raw, err := json.Marshal(map[string]any{"path": spelling})
			if err != nil {
				t.Fatalf("encoding the arguments: %v", err)
			}
			res, err := subject.Run(context.Background(), raw)
			if err != nil {
				t.Fatalf("read %s: %v", spelling, err)
			}
			assertOK(t, res)
			if _, ok := reads.LastRead("sub/main.go"); !ok {
				t.Errorf("reading %s recorded something other than sub/main.go", spelling)
			}
		})
	}
}

func TestAFailedReadRecordsNothing(t *testing.T) {
	h := newHarness(t)
	h.write(t, "sub/file.txt", "x\n")

	for _, path := range []string{"nowhere.go", "../outside/secret.txt", "sub"} {
		res := h.call(t, map[string]any{"path": path})
		if !res.IsError {
			t.Fatalf("read %q succeeded, so this test is no longer about a failed read", path)
		}
	}
	for _, key := range []string{"nowhere.go", "../outside/secret.txt", "sub", "secret.txt"} {
		if _, ok := h.reads.LastRead(key); ok {
			t.Errorf("a failed read recorded %q; an edit would then believe the model had seen it", key)
		}
	}
}

func TestACancelledTurnStopsTheScan(t *testing.T) {
	h := newHarness(t)
	h.write(t, "long.txt", lines(20000))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := h.run(t, ctx, map[string]any{"path": "long.txt"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("read on a cancelled context returned (%+v, %v), want context.Canceled: a turn being "+
			"torn down is the tool failing to run, not a result the model can act on", res, err)
	}
	if res.Content != "" || res.IsError {
		t.Errorf("a cancelled read also produced the result %+v", res)
	}
}

func TestTheSchemaAsksForAPathAndOffersTheWindow(t *testing.T) {
	h := newHarness(t)

	if name := h.subject.Name(); name != "read" {
		t.Errorf("the tool is named %q; it ships as read, and the session log records that name", name)
	}

	schema := h.subject.Schema()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("the schema has no properties object: %v", schema)
	}
	for _, want := range []string{"path", "offset", "limit"} {
		if _, ok := properties[want]; !ok {
			t.Errorf("the schema has no %q property: %v", want, properties)
		}
	}
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "path" {
		t.Errorf("required is %v, want exactly [path]: a window is optional and a file to read is not", schema["required"])
	}
}

func TestNewReadRefusesToBuildWithoutWhatItNeeds(t *testing.T) {
	h := newHarness(t)

	cases := []struct {
		what  string
		build func()
	}{
		{"no workspace", func() { builtin.NewRead(nil, h.reads) }},
		{"no tracker", func() { builtin.NewRead(h.ws, nil) }},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("NewRead built a tool that would fail on its first call instead of at startup")
				}
			}()
			c.build()
		})
	}
}

func assertOK(t *testing.T, res tool.Result) {
	t.Helper()

	if res.IsError {
		t.Fatalf("the read failed: %s", res.Content)
	}
	if res.Title == "" {
		t.Error("the result carries no Title, which is the tool card's one-line summary")
	}
	if _, ok := res.Details.(*builtin.ReadDetails); !ok {
		t.Errorf("Details is %T, want *builtin.ReadDetails for the UI to render", res.Details)
	}
}

func assertFailed(t *testing.T, res tool.Result, mustSay ...string) {
	t.Helper()

	if !res.IsError {
		t.Fatalf("the read succeeded, returning %q", res.Content)
	}
	if len(mustSay) == 0 {
		t.Fatal("assertFailed was given nothing the message must say, so it only checked the flag")
	}
	for _, want := range mustSay {
		if !strings.Contains(res.Content, want) {
			t.Errorf("the failure message %q does not mention %q", res.Content, want)
		}
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()

	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("reading %q as a number: %v", s, err)
	}
	return n
}
