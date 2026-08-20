package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tool/builtin"
	"github.com/theocod3s/rasp/internal/workspace"
)

func TestWriteCreatesTheFileAndItsParentDirectories(t *testing.T) {
	f := newWriteFixture(t)

	res := f.write("pkg/sub/new.txt", "hello")
	if res.IsError {
		t.Fatalf("write failed: %s", res.Content)
	}
	f.wantContent("pkg/sub/new.txt", "hello")

	// A file that was not there is a diff of every line added and none removed.
	details := f.details(res)
	if details.Additions != 1 || details.Deletions != 0 {
		t.Errorf("Details is +%d -%d, want +1 -0 for a file that did not exist",
			details.Additions, details.Deletions)
	}
	if !strings.Contains(details.Unified, "+hello") {
		t.Errorf("Details.Unified is %q, and does not add the line the file now holds", details.Unified)
	}
	if details.Path != "pkg/sub/new.txt" {
		t.Errorf("Details.Path = %q, want the workspace-relative path", details.Path)
	}

	// The model is told the two facts it can act on, since it never sees Details.
	if !strings.Contains(res.Content, "Created") || !strings.Contains(res.Content, "5 bytes") {
		t.Errorf("Content = %q, want it to report the creation and the byte count", res.Content)
	}
}

// TestWriteReturnsTheDiffOfWhatItReplaced is what makes a write drawable. The
// UI has no route to the file — it renders what Details carries and computes
// nothing — so a write that reported only a path and a byte count could not be
// drawn as a change at all.
func TestWriteReturnsTheDiffOfWhatItReplaced(t *testing.T) {
	f := newWriteFixture(t)
	f.seed("auth.go", "one\ntwo\nthree\n")

	res := f.write("auth.go", "one\nTWO\nthree\n")
	if res.IsError {
		t.Fatalf("write failed: %s", res.Content)
	}

	details := f.details(res)
	if details.Additions != 1 || details.Deletions != 1 {
		t.Errorf("Details is +%d -%d, want +1 -1 for one line replaced", details.Additions, details.Deletions)
	}
	for _, line := range []string{"-two", "+TWO"} {
		if !strings.Contains(details.Unified, line) {
			t.Errorf("Details.Unified does not hold %q:\n%s", line, details.Unified)
		}
	}
	// The lines that did not change are in it too, or the diff has no context to
	// place the one that did.
	if !strings.Contains(details.Unified, " one") {
		t.Errorf("Details.Unified carries no context:\n%s", details.Unified)
	}
	if details.Fuzzy {
		t.Error("Details.Fuzzy is set; a write matches nothing, so nothing about it can be approximate")
	}
}

// TestWriteSurvivesTheFileBeingTakenAwayMidCall. The per-file lock is rasp's
// own, so it orders rasp's writes and nothing else: an editor saving, a git
// checkout or a `make clean` can take the file away between the stat and the
// read the diff is taken from. That is the creation case arriving late, and
// refusing it would fail a write that used to succeed, for a reason the model
// can do nothing about.
func TestWriteSurvivesTheFileBeingTakenAwayMidCall(t *testing.T) {
	f := newWriteFixture(t)
	f.seed("notes.txt", "the old contents")

	// A mode the umask default is not, so the assertion below can tell "the mode
	// of the file that left" from "the mode a new file gets".
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(f.dir, "notes.txt"), 0o600); err != nil {
			t.Fatalf("chmod: %v", err)
		}
	}

	// Once: the tool stats again after the write to record what it left behind,
	// and a second removal would delete the file this test is about to read.
	var once sync.Once
	f.spy.afterStat = func(name string) {
		once.Do(func() {
			if err := os.Remove(filepath.Join(f.dir, filepath.FromSlash(name))); err != nil {
				t.Errorf("taking %s away mid-call: %v", name, err)
			}
		})
	}

	res := f.write("notes.txt", "the new contents")
	if res.IsError {
		t.Fatalf("the write was refused because the file went away: %s", res.Content)
	}
	f.wantContent("notes.txt", "the new contents")

	if d := f.details(res); d.Deletions != 0 {
		t.Errorf("Details is +%d -%d, and there was nothing left to delete by the time it was read",
			d.Additions, d.Deletions)
	}
	// And the call says so. Reporting a replacement would name a file this write
	// never touched, and it is the same flag that decides whether the new file
	// is given the mode of the one that has gone.
	if !strings.Contains(res.Content, "Created") {
		t.Errorf("Content = %q, want it to report a creation: there was nothing there to replace", res.Content)
	}

	// And the file it wrote is a new file's, not the departed one's. Preserving
	// 0o600 here would carry a mode forward from a file nothing on disk has any
	// more, which is the same mistake in the other direction.
	if runtime.GOOS != "windows" {
		if want := 0o666 &^ umaskOf(t, f.dir); f.mode("notes.txt") != want {
			t.Errorf("mode is %v, want the %v a plain create gets: the file it was read from is gone",
				f.mode("notes.txt"), want)
		}
	}
}

// TestWriteRefusesAFileThatArrivedMidCall is the other direction of the same
// race, and it is the dangerous one. The stat said the path was empty, so the
// read-before-overwrite guard was skipped; a file then appeared. Going ahead
// would destroy contents this session has never seen — the exact thing the
// tracker exists to refuse — and it would do it having had the bytes in hand.
func TestWriteRefusesAFileThatArrivedMidCall(t *testing.T) {
	f := newWriteFixture(t)

	// Nothing at the path when the stat runs; a file there by the time the
	// contents are read.
	var once sync.Once
	f.spy.afterStat = func(name string) {
		once.Do(func() {
			full := filepath.Join(f.dir, filepath.FromSlash(name))
			if err := os.WriteFile(full, []byte("somebody else's line\n"), 0o644); err != nil {
				t.Errorf("putting %s there mid-call: %v", name, err)
			}
		})
	}

	res := f.write("arrived.txt", "our line\n")
	if !res.IsError {
		t.Fatalf("the write went ahead over a file it had never read: %+v", res)
	}
	if !strings.Contains(res.Content, "has not been read") {
		t.Errorf("Content = %q, want it to say the file was never read", res.Content)
	}
	f.wantContent("arrived.txt", "somebody else's line\n")
}

// TestWriteDoesNotReadABigFileBackJustToDrawIt. The read this tool does exists
// only to build a card, so it is the one read here that must be bounded: a
// generated file of a few hundred megabytes would otherwise be pulled into
// memory, copied to a string, split into lines, and its diff held for the rest
// of the session — all under the per-file lock, and none of it visible.
func TestWriteDoesNotReadABigFileBackJustToDrawIt(t *testing.T) {
	f := newWriteFixture(t)
	f.seed("generated.txt", strings.Repeat("a line of a generated file\n", 12_000))

	var read []string
	f.spy.readFile = func(name string) ([]byte, error) {
		read = append(read, name)
		return f.spy.Workspace.ReadFile(name)
	}

	res := f.write("generated.txt", "one line now\n")
	if res.IsError {
		t.Fatalf("write failed: %s", res.Content)
	}
	f.wantContent("generated.txt", "one line now\n")

	if len(read) != 0 {
		t.Errorf("the tool read %v back to diff it", read)
	}
	if res.Details != nil {
		t.Errorf("Details is %#v, want nothing: no diff was taken", res.Details)
	}
	// And it is still a replacement, because a file too big to diff is still a
	// file that was there.
	if !strings.Contains(res.Content, "Replaced") {
		t.Errorf("Content = %q, want it to report the replacement", res.Content)
	}
}

// TestWriteStillWritesWhenTheOldContentsCannotBeRead. Details is drawn by the
// UI and read by nobody else, so failing to build one must not decide whether
// the file is written: replacing a file needs its directory, not the file, and
// a mode that forbids reading does not forbid the rename. The card falls back
// to saying what was written, which is honest — a diff against nothing would
// claim every line is new.
func TestWriteStillWritesWhenTheOldContentsCannotBeRead(t *testing.T) {
	f := newWriteFixture(t)
	f.seed("secret.txt", "the old contents")
	f.spy.readFile = func(string) ([]byte, error) { return nil, fs.ErrPermission }

	res := f.write("secret.txt", "the new contents")
	if res.IsError {
		t.Fatalf("the write was refused because its diff could not be built: %s", res.Content)
	}
	f.wantContent("secret.txt", "the new contents")

	if res.Details != nil {
		t.Errorf("Details is %#v, want nothing: a diff was drawn against contents nobody could read", res.Details)
	}
	if !strings.Contains(res.Content, "Replaced") {
		t.Errorf("Content = %q, want it to still report the replacement", res.Content)
	}
}

func TestWriteAtTheWorkspaceRoot(t *testing.T) {
	f := newWriteFixture(t)

	if res := f.write("root.txt", "at the top"); res.IsError {
		t.Fatalf("write failed: %s", res.Content)
	}
	f.wantContent("root.txt", "at the top")
}

// TestWriteCreatesWithTheModeAPlainCreateWould pins the new file's mode to
// whatever the process umask allows, rather than to the tighter mode the
// temporary file is created with.
func TestWriteCreatesWithTheModeAPlainCreateWould(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no umask and no mode bits to preserve")
	}
	f := newWriteFixture(t)
	want := 0o666 &^ umaskOf(t, f.dir)

	if res := f.write("new.txt", "x"); res.IsError {
		t.Fatalf("write failed: %s", res.Content)
	}
	if got := f.mode("new.txt"); got != want {
		t.Errorf("new file mode is %v, want %v — the mode a plain create gets under this umask", got, want)
	}
}

func TestWriteReplacesAndPreservesMode(t *testing.T) {
	f := newWriteFixture(t)
	f.seed("keep.sh", "old contents")

	// The mode carries the bits this umask strips, so an implementation that
	// only handed it to the create would land on a different one.
	mode := 0o666 | umaskOf(t, f.dir)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(f.dir, "keep.sh"), mode); err != nil {
			t.Fatalf("chmod: %v", err)
		}
	}

	res := f.write("keep.sh", "new contents")
	if res.IsError {
		t.Fatalf("write failed: %s", res.Content)
	}
	f.wantContent("keep.sh", "new contents")

	if runtime.GOOS != "windows" {
		if got := f.mode("keep.sh"); got != mode {
			t.Errorf("mode after overwrite is %v, want the %v the file had", got, mode)
		}
	}

	if details := f.details(res); details.Deletions == 0 {
		t.Errorf("Details is +%d -%d, and an overwrite takes the old line away",
			details.Additions, details.Deletions)
	}
	if !strings.Contains(res.Content, "Replaced") {
		t.Errorf("Content = %q, want it to say the file was replaced", res.Content)
	}
}

func TestWriteEmptyContentEmptiesTheFile(t *testing.T) {
	f := newWriteFixture(t)
	f.seed("notes.txt", "something")

	res := f.write("notes.txt", "")
	if res.IsError {
		t.Fatalf("write failed: %s", res.Content)
	}
	f.wantContent("notes.txt", "")
	if d := f.details(res); d.Additions != 0 || d.Deletions != 1 {
		t.Errorf("Details is +%d -%d, want the one line that was there taken away and nothing added",
			d.Additions, d.Deletions)
	}
	if !strings.Contains(res.Content, "0 bytes") {
		t.Errorf("Content = %q, want it to report the file is now empty", res.Content)
	}
}

// TestWriteRefusesACallWithNoContent covers the shape that costs a file: an
// absent content key decoded as "" would empty the file and report success.
func TestWriteRefusesACallWithNoContent(t *testing.T) {
	for _, raw := range []string{`{"path":"notes.txt"}`, `{"path":"notes.txt","content":null}`} {
		t.Run(raw, func(t *testing.T) {
			f := newWriteFixture(t)
			f.seed("notes.txt", "still here")

			res := f.raw(raw)
			if !res.IsError {
				t.Fatalf("Result is not an error: %+v", res)
			}
			f.wantContent("notes.txt", "still here")
		})
	}
}

func TestWriteRefusesAPathOutsideTheWorkspace(t *testing.T) {
	outside := t.TempDir()

	for name, path := range map[string]string{
		"parent":   "../escaped.txt",
		"deeper":   "sub/../../escaped.txt",
		"absolute": filepath.Join(outside, "escaped.txt"),
	} {
		t.Run(name, func(t *testing.T) {
			f := newWriteFixture(t)

			res := f.write(path, "should not land")
			if !res.IsError {
				t.Fatalf("Result is not an error: %+v", res)
			}
			if !strings.Contains(res.Content, "outside the workspace") {
				t.Errorf("Content = %q, want it to say the path is outside the workspace", res.Content)
			}
			f.wantTree()
		})
	}

	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("the directory beside the workspace holds %v (err %v), want nothing written there", entries, err)
	}
}

func TestWriteRefusesAPathThatIsNotAFile(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		f := newWriteFixture(t)
		if err := os.Mkdir(filepath.Join(f.dir, "sub"), 0o777); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		res := f.write("sub", "should not land")
		if !res.IsError {
			t.Fatalf("Result is not an error: %+v", res)
		}
		if !strings.Contains(res.Content, "directory") {
			t.Errorf("Content = %q, want it to say the path is a directory", res.Content)
		}
		f.wantTree()
	})

	// A parent that is a file is the same refusal from the other end, and it
	// reaches the filesystem rather than a check of ours.
	t.Run("parent is a file", func(t *testing.T) {
		f := newWriteFixture(t)
		f.seed("a.txt", "a file, not a directory")

		res := f.write("a.txt/b.txt", "should not land")
		if !res.IsError {
			t.Fatalf("Result is not an error: %+v", res)
		}
		f.wantContent("a.txt", "a file, not a directory")
		f.wantTree("a.txt")
	})
}

// TestWriteNeverOpensTheDestination is the structural half of atomicity: the
// bytes go to a temporary file in the destination's own directory — the same
// filesystem, or the rename would be a copy — and the destination changes only
// by rename. A tool that opened the destination with O_TRUNC would pass every
// content assertion in this file and still expose a half-written file.
func TestWriteNeverOpensTheDestination(t *testing.T) {
	f := newWriteFixture(t)
	f.seed("pkg/target.txt", "old")

	if res := f.write("pkg/target.txt", "new"); res.IsError {
		t.Fatalf("write failed: %s", res.Content)
	}

	if len(f.spy.opened) != 1 {
		t.Fatalf("opened %v, want exactly one file", f.spy.opened)
	}
	temp := f.spy.opened[0]
	if temp == "pkg/target.txt" {
		t.Fatalf("the write opened the destination itself")
	}
	if dir := filepath.ToSlash(filepath.Dir(temp)); dir != "pkg" {
		t.Errorf("the temporary file %q sits in %q, want the destination's own directory", temp, "pkg")
	}
	if want := [][2]string{{temp, "pkg/target.txt"}}; !slices.Equal(f.spy.renamed, want) {
		t.Errorf("renames = %v, want %v", f.spy.renamed, want)
	}
}

// TestWriteFailurePreservesTheFileAndLeavesNothingBehind fails a write at each
// point it can fail, and asserts the two things the mechanism promises: the
// destination is the old file or the new one, never a mix, and no temporary
// file survives to be read as source, committed, or found by grep.
func TestWriteFailurePreservesTheFileAndLeavesNothingBehind(t *testing.T) {
	refuse := errors.New("injected failure")

	for name, inject := range map[string]func(s *writeSpy){
		"creating the temporary file": func(s *writeSpy) {
			s.openFile = func(string, int, fs.FileMode) (*os.File, error) { return nil, refuse }
		},
		"writing to it": func(s *writeSpy) {
			// Handing back a read-only descriptor fails the write itself, after
			// the file exists — the one path that can leave a temporary behind.
			s.openFile = func(name string, flag int, perm fs.FileMode) (*os.File, error) {
				return s.Workspace.OpenFile(name, flag&^os.O_WRONLY, perm)
			}
		},
		"renaming it into place": func(s *writeSpy) {
			s.rename = func(string, string) error { return refuse }
		},
		"creating the parent directory": func(s *writeSpy) {
			s.mkdirAll = func(string, fs.FileMode) error { return refuse }
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newWriteFixture(t)
			f.seed("pkg/target.txt", "old")
			inject(f.spy)

			res := f.write("pkg/target.txt", "new")
			if !res.IsError {
				t.Fatalf("Result is not an error: %+v", res)
			}
			f.wantContent("pkg/target.txt", "old")
			f.wantTree("pkg/target.txt")
		})
	}
}

func TestWriteReportsACancelledTurnAsAnError(t *testing.T) {
	f := newWriteFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	res, err := f.tool.Run(ctx, json.RawMessage(`{"path":"new.txt","content":"x"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned (%+v, %v), want a cancellation error", res, err)
	}
	f.wantTree()
}

func TestWriteSchemaRequiresBothFields(t *testing.T) {
	f := newWriteFixture(t)

	required, _ := f.tool.Schema()["required"].([]string)
	slices.Sort(required)
	if want := []string{"content", "path"}; !slices.Equal(required, want) {
		t.Errorf("required = %v, want %v", required, want)
	}
}

func TestNewWriteRefusesToBuildWithoutWhatItNeeds(t *testing.T) {
	f := newWriteFixture(t)

	cases := []struct {
		what  string
		build func()
	}{
		{"no workspace", func() { builtin.NewWrite(nil, f.reads) }},
		{"no tracker", func() { builtin.NewWrite(f.spy, nil) }},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("NewWrite built a tool that would fail on its first call instead of at startup")
				}
			}()
			c.build()
		})
	}
}

// writeFixture is the write tool over a workspace holding nothing.
type writeFixture struct {
	t     *testing.T
	dir   string
	spy   *writeSpy
	reads *workspace.Tracker
	tool  tool.Tool
}

func newWriteFixture(t *testing.T) *writeFixture {
	t.Helper()

	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatalf("opening the workspace: %v", err)
	}
	t.Cleanup(func() { ws.Close() })

	spy := &writeSpy{Workspace: ws}
	reads := workspace.NewTracker()
	return &writeFixture{t: t, dir: dir, spy: spy, reads: reads, tool: builtin.NewWrite(spy, reads)}
}

func (f *writeFixture) write(path, content string) tool.Result {
	f.t.Helper()
	raw, err := json.Marshal(map[string]string{"path": path, "content": content})
	if err != nil {
		f.t.Fatalf("encoding the arguments: %v", err)
	}
	return f.raw(string(raw))
}

func (f *writeFixture) raw(raw string) tool.Result {
	f.t.Helper()
	res, err := f.tool.Run(f.t.Context(), json.RawMessage(raw))
	if err != nil {
		f.t.Fatalf("Run returned a Go error for %s: %v — a write the model can retry is a failed Result", raw, err)
	}
	return res
}

// details is the write's payload for the UI. The type assertion is the point:
// a write and an edit hand back the same shape, so one renderer draws both and
// neither tool knows a terminal exists.
func (f *writeFixture) details(res tool.Result) *tool.DiffDetails {
	f.t.Helper()
	details, ok := res.Details.(*tool.DiffDetails)
	if !ok {
		f.t.Fatalf("Details is %T, want *tool.DiffDetails", res.Details)
	}
	return details
}

// seed writes a file the tool did not, creating its parents, and records it in
// the fixture's tracker as read — standing in for a session that has already
// seen it, so a test seeding a file to overwrite is not incidentally a test of
// the read-before-edit guard too.
func (f *writeFixture) seed(rel, content string) {
	f.t.Helper()
	full := filepath.Join(f.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o777); err != nil {
		f.t.Fatalf("seeding %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		f.t.Fatalf("seeding %s: %v", rel, err)
	}
	info, err := os.Stat(full)
	if err != nil {
		f.t.Fatalf("stat %s: %v", rel, err)
	}
	f.reads.Record(filepath.ToSlash(rel), info.ModTime())
}

func (f *writeFixture) wantContent(rel, want string) {
	f.t.Helper()
	got, err := os.ReadFile(filepath.Join(f.dir, filepath.FromSlash(rel)))
	if err != nil {
		f.t.Fatalf("reading %s: %v", rel, err)
	}
	if string(got) != want {
		f.t.Errorf("%s holds %q, want %q", rel, got, want)
	}
}

func (f *writeFixture) mode(rel string) fs.FileMode {
	f.t.Helper()
	info, err := os.Stat(filepath.Join(f.dir, filepath.FromSlash(rel)))
	if err != nil {
		f.t.Fatalf("stat %s: %v", rel, err)
	}
	return info.Mode().Perm()
}

// umaskOf is the mode bits this process's umask strips from a file it creates,
// measured in dir rather than asked for: syscall.Umask is unix-only, and it
// answers by setting, which no test can do without disturbing the others.
func umaskOf(t *testing.T, dir string) fs.FileMode {
	t.Helper()
	if runtime.GOOS == "windows" {
		return 0
	}

	probe := filepath.Join(dir, ".umask-probe")
	if err := os.WriteFile(probe, nil, 0o777); err != nil {
		t.Fatalf("writing the umask probe: %v", err)
	}
	info, err := os.Stat(probe)
	if err != nil {
		t.Fatalf("stat of the umask probe: %v", err)
	}
	if err := os.Remove(probe); err != nil {
		t.Fatalf("removing the umask probe: %v", err)
	}
	return 0o777 &^ info.Mode().Perm()
}

// wantTree asserts the workspace holds exactly these files and no others, which
// is how a temporary file left behind by a failed write is caught. Directories
// are ignored: an empty one is not litter a later read can trip over.
func (f *writeFixture) wantTree(want ...string) {
	f.t.Helper()

	var found []string
	err := filepath.WalkDir(f.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(f.dir, p)
		if err != nil {
			return err
		}
		found = append(found, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		f.t.Fatalf("walking the workspace: %v", err)
	}

	slices.Sort(found)
	slices.Sort(want)
	if !slices.Equal(found, want) {
		f.t.Errorf("the workspace holds %v, want exactly %v", found, want)
	}
}

// writeSpy records what the tool asked the filesystem for, and fails whichever
// call a test hands it a replacement for.
type writeSpy struct {
	*workspace.Workspace

	openFile func(name string, flag int, perm fs.FileMode) (*os.File, error)
	rename   func(oldname, newname string) error
	mkdirAll func(name string, perm fs.FileMode) error

	// afterStat runs once the stat has answered, which is where a test stages
	// something happening to the file between the calls the tool makes about it.
	afterStat func(name string)
	readFile  func(name string) ([]byte, error)

	opened  []string
	renamed [][2]string
}

func (s *writeSpy) Stat(name string) (fs.FileInfo, error) {
	info, err := s.Workspace.Stat(name)
	if s.afterStat != nil {
		s.afterStat(name)
	}
	return info, err
}

func (s *writeSpy) ReadFile(name string) ([]byte, error) {
	if s.readFile != nil {
		return s.readFile(name)
	}
	return s.Workspace.ReadFile(name)
}

func (s *writeSpy) OpenFile(name string, flag int, perm fs.FileMode) (*os.File, error) {
	s.opened = append(s.opened, name)
	if s.openFile != nil {
		return s.openFile(name, flag, perm)
	}
	return s.Workspace.OpenFile(name, flag, perm)
}

func (s *writeSpy) Rename(oldname, newname string) error {
	s.renamed = append(s.renamed, [2]string{oldname, newname})
	if s.rename != nil {
		return s.rename(oldname, newname)
	}
	return s.Workspace.Rename(oldname, newname)
}

func (s *writeSpy) MkdirAll(name string, perm fs.FileMode) error {
	if s.mkdirAll != nil {
		return s.mkdirAll(name, perm)
	}
	return s.Workspace.MkdirAll(name, perm)
}
