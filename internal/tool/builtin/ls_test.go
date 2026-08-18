package builtin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tool/builtin"
	"github.com/theocod3s/rasp/internal/workspace"
)

type lsFixture struct {
	dir     string // the workspace root as passed to Open
	outside string // a directory beside it, off limits
	ws      *workspace.Workspace
	subject tool.Tool
}

func newLsFixture(t *testing.T) lsFixture {
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
	return lsFixture{dir: dir, outside: outside, ws: ws, subject: builtin.NewLs(ws)}
}

func (f lsFixture) mkdir(t *testing.T, rel string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Join(f.dir, filepath.FromSlash(rel)), 0o755); err != nil {
		t.Fatalf("creating the directory %s: %v", rel, err)
	}
}

func (f lsFixture) file(t *testing.T, rel, content string) {
	t.Helper()

	path := filepath.Join(f.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating the parent of %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", rel, err)
	}
}

// list runs the tool the way the loop does, through the JSON the model sends. A
// Go error is fatal: a directory it cannot list is a failed result, not a failed
// tool (design §3.4).
func (f lsFixture) list(t *testing.T, args map[string]any) tool.Result {
	t.Helper()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encoding the arguments %v: %v", args, err)
	}
	res, err := f.subject.Run(context.Background(), raw)
	if err != nil {
		t.Fatalf("ls(%v) returned the Go error %v, not a result", args, err)
	}
	return res
}

func assertLsOK(t *testing.T, res tool.Result) *builtin.LsDetails {
	t.Helper()

	if res.IsError {
		t.Fatalf("the listing failed: %s", res.Content)
	}
	if res.Title == "" {
		t.Error("the result carries no Title, which is the tool card's one-line summary")
	}
	details, ok := res.Details.(*builtin.LsDetails)
	if !ok {
		t.Fatalf("Details is %T, want *builtin.LsDetails for the UI to render", res.Details)
	}
	return details
}

// lsLines is the listing without its header, one entry per line.
func lsLines(t *testing.T, content string) []string {
	t.Helper()

	_, rest, ok := strings.Cut(content, "\n")
	if !ok {
		t.Fatalf("the listing %q has no header line, so there is nothing to strip", content)
	}
	rest = strings.TrimSuffix(rest, "\n")
	if rest == "" {
		return nil
	}
	return strings.Split(rest, "\n")
}

func TestLsNamesEveryEntryWithWhatItIsAndHowBigItIs(t *testing.T) {
	f := newLsFixture(t)
	f.mkdir(t, "cmd")
	f.file(t, "README.md", "# rasp\n")
	f.file(t, "go.mod", "module x\n")

	res := f.list(t, map[string]any{"path": "."})
	details := assertLsOK(t, res)

	want := "The workspace root has 3 entries:\ncmd\tdir\nREADME.md\t7 B\ngo.mod\t9 B\n"
	if res.Content != want {
		t.Errorf("ls . returned\n%q\nwant\n%q", res.Content, want)
	}
	if got := len(details.Entries); got != 3 || details.Total != 3 {
		t.Errorf("Details holds %d of %d entries, want 3 of 3", got, details.Total)
	}
}

// TestLsSizesAreTheBytesOnDisk compares against the file's own length rather
// than a literal, so a size column that started rounding, or reporting the
// directory entry rather than the file, has nowhere to hide.
func TestLsSizesAreTheBytesOnDisk(t *testing.T) {
	f := newLsFixture(t)
	for _, n := range []int{0, 1, 999, 40000} {
		f.file(t, fmt.Sprintf("size-%05d.txt", n), strings.Repeat("x", n))
	}

	res := f.list(t, map[string]any{"path": "."})
	details := assertLsOK(t, res)

	if len(details.Entries) != 4 {
		t.Fatalf("the listing has %d entries, want the 4 written: %q", len(details.Entries), res.Content)
	}
	for _, e := range details.Entries {
		info, err := os.Stat(filepath.Join(f.dir, e.Name))
		if err != nil {
			t.Fatalf("stat %s: %v", e.Name, err)
		}
		if e.Size != info.Size() {
			t.Errorf("%s is listed at %d bytes, want its own %d", e.Name, e.Size, info.Size())
		}
		if line := fmt.Sprintf("%s\t%d B", e.Name, info.Size()); !strings.Contains(res.Content, line) {
			t.Errorf("the listing has no line %q:\n%s", line, res.Content)
		}
	}
}

func TestLsDoesNotDescend(t *testing.T) {
	f := newLsFixture(t)
	f.file(t, "internal/deep/buried.go", "package deep\n")
	f.file(t, "top.go", "package main\n")

	res := f.list(t, map[string]any{"path": "."})
	assertLsOK(t, res)

	if lines := lsLines(t, res.Content); len(lines) != 2 {
		t.Errorf("ls . returned %d entries, want the 2 directly inside it:\n%s", len(lines), res.Content)
	}
	for _, buried := range []string{"buried.go", "deep"} {
		if strings.Contains(res.Content, buried) {
			t.Errorf("the listing reaches past the directory it was asked for, naming %q:\n%s", buried, res.Content)
		}
	}
}

// TestLsOrdersDirectoriesFirstThenByName pins the whole order, not the presence
// of the names. The filesystem hands a directory back in its own storage order,
// so the ordering here is the tool's and nothing else's.
func TestLsOrdersDirectoriesFirstThenByName(t *testing.T) {
	f := newLsFixture(t)
	// Created back to front, so a listing that echoed creation order would be
	// exactly reversed rather than accidentally right.
	for _, name := range []string{"zeta.go", "beta.go", "alpha.go"} {
		f.file(t, name, "x")
	}
	for _, name := range []string{"zoo", "bin", "api"} {
		f.mkdir(t, name)
	}

	res := f.list(t, map[string]any{"path": "."})
	assertLsOK(t, res)

	var got []string
	for _, line := range lsLines(t, res.Content) {
		name, _, _ := strings.Cut(line, "\t")
		got = append(got, name)
	}
	want := []string{"api", "bin", "zoo", "alpha.go", "beta.go", "zeta.go"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("ls . listed %v, want %v: directories first, each group by name", got, want)
	}
}

func TestLsOfAnEmptyDirectorySaysSoInWords(t *testing.T) {
	for _, c := range []struct{ what, path, say string }{
		{"a subdirectory", "empty", "empty"},
		{"the workspace root", ".", "workspace root"},
	} {
		t.Run(c.what, func(t *testing.T) {
			f := newLsFixture(t)
			if c.path != "." {
				f.mkdir(t, c.path)
			}

			res := f.list(t, map[string]any{"path": c.path})
			details := assertLsOK(t, res)

			if res.Content == "" {
				t.Fatal("an empty directory returned empty content, which providers refuse as a tool result")
			}
			if !strings.Contains(res.Content, c.say) || !strings.Contains(res.Content, "nothing in it") {
				t.Errorf("listing the empty %s says %q, which does not say in words that it is empty", c.path, res.Content)
			}
			if len(details.Entries) != 0 {
				t.Errorf("the empty directory came back with the entries %v", details.Entries)
			}
		})
	}
}

func TestLsWithNoPathListsTheWorkspaceRoot(t *testing.T) {
	f := newLsFixture(t)
	f.file(t, "at-the-root.txt", "x")

	for _, what := range []string{"no arguments at all", "an empty path"} {
		t.Run(what, func(t *testing.T) {
			args := map[string]any{}
			if what == "an empty path" {
				args["path"] = ""
			}
			res := f.list(t, args)
			details := assertLsOK(t, res)

			if details.Path != "." {
				t.Errorf("ls with %s listed %q, want the workspace root", what, details.Path)
			}
			if !strings.Contains(res.Content, "at-the-root.txt") {
				t.Errorf("ls with %s returned %q, which does not name the file at the root", what, res.Content)
			}
		})
	}
}

func TestLsOutsideTheWorkspaceIsRefused(t *testing.T) {
	f := newLsFixture(t)

	// The last is a file rather than a directory, and still comes back as an
	// escape: the confinement answers before anything is known about the target.
	for _, path := range []string{"../outside", f.outside, filepath.Join(f.outside, "secret.txt")} {
		res := f.list(t, map[string]any{"path": path})
		assertLsFailed(t, res, "outside the workspace")
	}
	// Separately, because the third path names the file itself.
	for _, path := range []string{"../outside", f.outside} {
		res := f.list(t, map[string]any{"path": path})
		if strings.Contains(res.Content, "secret.txt") {
			t.Errorf("the refusal for %s lists what is inside it: %q", path, res.Content)
		}
	}
}

// TestLsThroughASymlinkOutOfTheWorkspaceIsRefused is the escape lexical checking
// cannot see: nothing in "escape" says it leaves the root, and os.Root refuses it
// while resolving.
func TestLsThroughASymlinkOutOfTheWorkspaceIsRefused(t *testing.T) {
	f := newLsFixture(t)
	lsSymlink(t, f.outside, filepath.Join(f.dir, "escape"))

	res := f.list(t, map[string]any{"path": "escape"})
	assertLsFailed(t, res, "outside the workspace")
	if strings.Contains(res.Content, "secret.txt") {
		t.Errorf("the refusal listed what is on the other side of the link: %q", res.Content)
	}
}

// TestLsListsASymlinkWithoutFollowingIt: the link is a name in the directory and
// belongs in the listing, but calling it a file would tell the model a size and a
// kind belonging to whatever it points at — which may be outside the workspace.
func TestLsListsASymlinkWithoutFollowingIt(t *testing.T) {
	f := newLsFixture(t)
	lsSymlink(t, filepath.Join(f.outside, "secret.txt"), filepath.Join(f.dir, "escape"))

	res := f.list(t, map[string]any{"path": "."})
	details := assertLsOK(t, res)

	if len(details.Entries) != 1 || details.Entries[0].Kind != builtin.LsSymlink {
		t.Fatalf("the listing is %v, want the one entry as a symlink:\n%s", details.Entries, res.Content)
	}
	if want := "escape\tsymlink\n"; !strings.Contains(res.Content, want) {
		t.Errorf("the listing is %q, want a line %q", res.Content, want)
	}
}

func TestLsOfADirectoryThatIsNotThereIsAFailedResult(t *testing.T) {
	f := newLsFixture(t)

	res := f.list(t, map[string]any{"path": "nowhere"})
	assertLsFailed(t, res, "nowhere")
}

func TestLsOfAFileSaysToUseRead(t *testing.T) {
	f := newLsFixture(t)
	f.file(t, "main.go", "package main\n")

	res := f.list(t, map[string]any{"path": "main.go"})
	assertLsFailed(t, res, "not a directory", "read")
}

// TestLsBoundsALongListing: a listing of a node_modules is a context window
// spent on filenames. The cap has to be honest about it — the count in the header
// is what is there, and the trailer says how much of it did not come back.
func TestLsBoundsALongListing(t *testing.T) {
	f := newLsFixture(t)

	const written = 1200
	f.mkdir(t, "many")
	for i := range written {
		f.file(t, fmt.Sprintf("many/f%05d.txt", i), "x")
	}

	res := f.list(t, map[string]any{"path": "many"})
	details := assertLsOK(t, res)

	trailer := regexp.MustCompile(`\.\.\. and (\d+) entries not listed`).FindStringSubmatch(res.Content)
	if trailer == nil {
		t.Fatalf("%d entries came back whole, so the cap is above that and this test measured nothing. "+
			"Raise the count it writes past the cap: %q", written, lastLine(res.Content))
	}
	omitted := lsAtoi(t, trailer[1])

	listed := len(lsLines(t, res.Content)) - 1 // the trailer is not an entry
	if listed+omitted != written {
		t.Errorf("the listing shows %d entries and says %d are missing, which is %d of the %d written",
			listed, omitted, listed+omitted, written)
	}
	if !strings.Contains(res.Content, fmt.Sprintf("has %d entries", written)) {
		t.Errorf("the header of a capped listing does not say how many entries there are: %q", firstLine(res.Content))
	}
	if details.Total != written || len(details.Entries) != listed {
		t.Errorf("Details holds %d of %d entries, want %d of %d", len(details.Entries), details.Total, listed, written)
	}
}

// TestLsQuotesANameThatWouldSpanTwoLines: one entry per line means a newline in a
// filename invents an entry that does not exist.
func TestLsQuotesANameThatWouldSpanTwoLines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses a filename containing a control character")
	}
	f := newLsFixture(t)
	f.file(t, "two\nlines.txt", "x")

	res := f.list(t, map[string]any{"path": "."})
	assertLsOK(t, res)

	if lines := lsLines(t, res.Content); len(lines) != 1 {
		t.Errorf("one file produced %d entry lines:\n%q", len(lines), res.Content)
	}
	if !strings.Contains(res.Content, `"two\nlines.txt"`) {
		t.Errorf("the listing is %q, want the name quoted", res.Content)
	}
}

func TestLsSchemaAsksForNothingItDoesNotNeed(t *testing.T) {
	f := newLsFixture(t)

	if name := f.subject.Name(); name != "ls" {
		t.Errorf("the tool is named %q; prd §6.2 names it ls, and the session log records that name", name)
	}

	schema := f.subject.Schema()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("the schema has no properties object: %v", schema)
	}
	if _, ok := properties["path"]; !ok {
		t.Errorf("the schema has no path property: %v", properties)
	}
	if required, ok := schema["required"]; ok {
		t.Errorf("required is %v, want nothing: ls with no path lists the workspace root", required)
	}
}

func TestNewLsRefusesToBuildWithoutAWorkspace(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewLs built a tool that would fail on its first call instead of at startup")
		}
	}()
	builtin.NewLs(nil)
}

func assertLsFailed(t *testing.T, res tool.Result, mustSay ...string) {
	t.Helper()

	if !res.IsError {
		t.Fatalf("the listing succeeded, returning %q", res.Content)
	}
	if len(mustSay) == 0 {
		t.Fatal("assertLsFailed was given nothing the message must say, so it only checked the flag")
	}
	for _, want := range mustSay {
		if !strings.Contains(res.Content, want) {
			t.Errorf("the failure message %q does not mention %q", res.Content, want)
		}
	}
}

// lsSymlink creates one, or skips the test where the platform will not allow it.
// Windows needs developer mode or an elevated process.
func lsSymlink(t *testing.T, target, link string) {
	t.Helper()

	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating a symlink needs privileges on Windows: %v", err)
		}
		t.Fatalf("linking %s -> %s: %v", link, target, err)
	}
}

func lsAtoi(t *testing.T, s string) int {
	t.Helper()

	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatalf("reading %q as a number: %v", s, err)
	}
	return n
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	return lines[len(lines)-1]
}
