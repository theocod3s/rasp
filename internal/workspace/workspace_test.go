package workspace_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/workspace"
)

// fixture is a workspace with one file in it, alongside a sibling directory
// holding a file the workspace must never reach.
type fixture struct {
	ws      *workspace.Workspace
	dir     string // the workspace root, as passed to Open
	outside string // a directory beside it, off limits
}

func newFixture(t *testing.T) fixture {
	t.Helper()

	base := t.TempDir()
	dir := filepath.Join(base, "ws")
	outside := filepath.Join(base, "outside")
	mkdir(t, filepath.Join(dir, "sub"))
	mkdir(t, outside)
	write(t, filepath.Join(dir, "inside.txt"), "inside")
	write(t, filepath.Join(outside, "secret.txt"), "secret")

	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatalf("opening the workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := ws.Close(); err != nil {
			t.Errorf("closing the workspace: %v", err)
		}
	})
	return fixture{ws: ws, dir: dir, outside: outside}
}

func TestResolveAcceptsPathsInsideTheWorkspace(t *testing.T) {
	f := newFixture(t)

	cases := []struct {
		what  string
		given string
		want  string
	}{
		{"a bare name", "inside.txt", "inside.txt"},
		{"a dot-prefixed name", "./inside.txt", "inside.txt"},
		{"a nested name", "sub/deeper.txt", "sub/deeper.txt"},
		{"dot-dot that stays inside", "sub/../inside.txt", "inside.txt"},
		{"the root itself", ".", "."},
		{"a trailing separator", "sub/", "sub"},
		{"an absolute path inside", filepath.Join(f.ws.Root(), "inside.txt"), "inside.txt"},
		{"an absolute path to the root", f.ws.Root(), "."},
	}

	for _, c := range cases {
		got, err := f.ws.Resolve(c.given)
		if err != nil {
			t.Errorf("%s: Resolve(%q) = %v, want it accepted", c.what, c.given, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: Resolve(%q) = %q, want %q", c.what, c.given, got, c.want)
		}
	}
}

func TestDotDotEscapeIsRejected(t *testing.T) {
	f := newFixture(t)

	for _, given := range []string{
		"..",
		"../outside/secret.txt",
		"../../../etc/passwd",
		"sub/../../outside/secret.txt",
		"./../outside/secret.txt",
		filepath.Join(f.outside, "secret.txt"), // absolute, and elsewhere
	} {
		_, err := f.ws.Resolve(given)
		assertOutside(t, err, given, f.ws.Root())

		// Refusing in Resolve and refusing at the point of use are separate paths
		// through this package, and a tool that never calls Resolve has to get the
		// same answer.
		if _, err := f.ws.ReadFile(given); !errors.Is(err, workspace.ErrOutside) {
			t.Errorf("ReadFile(%q) = %v, want ErrOutside", given, err)
		}
	}
}

// TestSymlinkOutOfTheWorkspaceIsRejected is the case lexical path checking cannot
// see: nothing in "escape-abs" says it leaves the root. os.Root refuses it while
// resolving, which is what prd §6.6 means by leaving this to the runtime.
func TestSymlinkOutOfTheWorkspaceIsRejected(t *testing.T) {
	f := newFixture(t)

	secret := filepath.Join(f.outside, "secret.txt")
	symlink(t, secret, filepath.Join(f.dir, "escape-abs"))
	symlink(t, filepath.Join("..", "outside", "secret.txt"), filepath.Join(f.dir, "escape-rel"))
	symlink(t, f.outside, filepath.Join(f.dir, "escape-dir"))

	// Reads and writes both: a write through an escaping link would modify a file
	// outside the workspace, which is the worse half. Each op returns the path it
	// supplied, because the two that do not follow a final symlink have to reach
	// through the link to escape at all — Remove unlinks the link itself, and
	// MkdirAll fails on it with EEXIST, so neither is an escape by that name.
	ops := []struct {
		name string
		run  func(link string) (string, error)
	}{
		{"ReadFile", func(link string) (string, error) { _, err := f.ws.ReadFile(link); return link, err }},
		{"Stat", func(link string) (string, error) { _, err := f.ws.Stat(link); return link, err }},
		{"Open", func(link string) (string, error) {
			file, err := f.ws.Open(link)
			if err == nil {
				file.Close()
			}
			return link, err
		}},
		{"WriteFile", func(link string) (string, error) {
			return link, f.ws.WriteFile(link, []byte("clobbered"), 0o644)
		}},
		{"ReadDir", func(link string) (string, error) { _, err := f.ws.ReadDir(link); return link, err }},
		{"MkdirAll", func(link string) (string, error) {
			name := link + "/new"
			return name, f.ws.MkdirAll(name, 0o755)
		}},
		{"Remove", func(link string) (string, error) {
			name := link + "/secret.txt"
			return name, f.ws.Remove(name)
		}},
		// Both directions, because os reports a rename failure over both paths
		// and leaves the package to work out which one it objected to.
		{"Rename into", func(link string) (string, error) {
			name := link + "/moved.txt"
			return name, f.ws.Rename("inside.txt", name)
		}},
		{"Rename out of", func(link string) (string, error) {
			name := link + "/secret.txt"
			return name, f.ws.Rename(name, "stolen.txt")
		}},
	}

	for _, link := range []string{"escape-abs", "escape-rel", "escape-dir", "escape-dir/secret.txt"} {
		for _, op := range ops {
			given, err := op.run(link)
			if !errors.Is(err, workspace.ErrOutside) {
				t.Errorf("%s(%q) = %v, want ErrOutside. os.Root exports no sentinel for its escape "+
					"refusal, so this package recognises it by the text %q — a Go release that "+
					"reworded that lands here", op.name, given, err, "path escapes from parent")
				continue
			}
			assertOutside(t, err, given, f.ws.Root())
		}
	}

	// The file outside is still what it was: nothing above wrote through a link.
	if got := read(t, secret); got != "secret" {
		t.Errorf("the file outside the workspace now reads %q, want %q", got, "secret")
	}
}

// TestSymlinkInsideTheWorkspaceIsFollowed is the negative control for the test
// above: refusing every symlink would pass it while breaking ordinary
// repositories, where a link to a sibling file is unremarkable.
func TestSymlinkInsideTheWorkspaceIsFollowed(t *testing.T) {
	f := newFixture(t)
	symlink(t, "inside.txt", filepath.Join(f.dir, "ok-link"))
	symlink(t, filepath.Join("..", "inside.txt"), filepath.Join(f.dir, "sub", "up-link"))

	for _, given := range []string{"ok-link", "sub/up-link"} {
		got, err := f.ws.ReadFile(given)
		if err != nil {
			t.Errorf("ReadFile(%q) = %v, want it followed", given, err)
			continue
		}
		if string(got) != "inside" {
			t.Errorf("ReadFile(%q) = %q, want %q", given, got, "inside")
		}
	}
}

func TestEmptyPathIsRejectedWithoutBlamingConfinement(t *testing.T) {
	f := newFixture(t)

	_, err := f.ws.Resolve("")
	if err == nil {
		t.Fatal(`Resolve("") succeeded, want an error`)
	}
	if errors.Is(err, workspace.ErrOutside) {
		t.Errorf(`Resolve("") = %v, reported as an escape; an empty path is a malformed request, `+
			`and saying otherwise sends the caller hunting a confinement problem`, err)
	}
}

// TestMissingFileIsNotAnEscape guards the classification rather than the refusal.
// Every failure this package wraps arrives as the same *fs.PathError, so an
// over-eager escape check would rename "no such file" into a confinement
// violation and send the model looking in the wrong place.
func TestMissingFileIsNotAnEscape(t *testing.T) {
	f := newFixture(t)

	_, err := f.ws.ReadFile("sub/nothing.txt")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFile of a missing file = %v, want it to match fs.ErrNotExist", err)
	}
	if errors.Is(err, workspace.ErrOutside) {
		t.Errorf("ReadFile of a missing file = %v, reported as an escape", err)
	}
	if err != nil && !strings.Contains(err.Error(), "sub/nothing.txt") {
		t.Errorf("error %q does not name the path that failed", err)
	}
}

func TestOperationsStayInsideTheRoot(t *testing.T) {
	f := newFixture(t)

	if err := f.ws.MkdirAll("a/b/c", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := f.ws.WriteFile("a/b/c/note.txt", []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := f.ws.ReadFile("a/b/c/note.txt")
	if err != nil || string(got) != "hello" {
		t.Fatalf("ReadFile = %q, %v; want %q", got, err, "hello")
	}
	if onDisk := read(t, filepath.Join(f.dir, "a", "b", "c", "note.txt")); onDisk != "hello" {
		t.Errorf("the file on disk reads %q, want %q — the write did not land under the root",
			onDisk, "hello")
	}

	info, err := f.ws.Stat("a/b/c/note.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() != int64(len("hello")) {
		t.Errorf("Stat reports %d bytes, want %d", info.Size(), len("hello"))
	}

	entries, err := f.ws.ReadDir("a/b/c")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "note.txt" {
		t.Errorf("ReadDir = %v, want just note.txt", entries)
	}

	if err := f.ws.Rename("a/b/c/note.txt", "a/b/c/renamed.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := f.ws.Remove("a/b/c/renamed.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := f.ws.Stat("a/b/c/renamed.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after Remove, Stat = %v, want fs.ErrNotExist", err)
	}

	file, err := f.ws.OpenFile("exclusive.txt", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	_, err = f.ws.OpenFile("exclusive.txt", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("a second exclusive create = %v, want fs.ErrExist", err)
	}
}

// TestRenameNamesWhicheverSideEscapes matters because a rename has two paths, and
// a wrapper that only ever reports the first tells the caller about the innocent
// one half the time.
func TestRenameNamesWhicheverSideEscapes(t *testing.T) {
	f := newFixture(t)
	escape := filepath.Join("..", "outside", "stolen.txt")

	assertOutside(t, f.ws.Rename("inside.txt", escape), escape, f.ws.Root())
	assertOutside(t, f.ws.Rename(escape, "inside.txt"), escape, f.ws.Root())
}

func TestFSWalkSeesOnlyTheWorkspace(t *testing.T) {
	f := newFixture(t)
	symlink(t, f.outside, filepath.Join(f.dir, "escape-dir"))

	var walked []string
	err := fs.WalkDir(f.ws.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		walked = append(walked, p)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the workspace: %v", err)
	}

	// The link itself is an entry; what must not appear is anything beneath it,
	// which the walk could only have found by following it out of the root.
	for _, p := range walked {
		if strings.HasPrefix(p, "escape-dir/") {
			t.Errorf("the walk descended through a symlink out of the workspace: %q", p)
		}
	}
	if !slices.Contains(walked, "inside.txt") {
		t.Errorf("the walk found %v, which does not include the file inside the workspace", walked)
	}
}

// TestAbsAndRootAreCanonical pins the two properties the per-file mutation mutex
// will key on: a symlink-resolved root, and an absolute path built from it.
func TestAbsAndRootAreCanonical(t *testing.T) {
	f := newFixture(t)

	resolved := evalSymlinks(t, f.dir)
	if f.ws.Root() != resolved {
		t.Errorf("Root() = %q, want the symlink-resolved %q", f.ws.Root(), resolved)
	}

	got, err := f.ws.Abs("sub/../inside.txt")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if want := filepath.Join(resolved, "inside.txt"); got != want {
		t.Errorf("Abs = %q, want %q", got, want)
	}

	if _, err := f.ws.Abs("../outside/secret.txt"); !errors.Is(err, workspace.ErrOutside) {
		t.Errorf("Abs of an escaping path = %v, want ErrOutside", err)
	}
}

// TestRootReachedThroughASymlinkAcceptsBothSpellings is why Workspace keeps the
// pre-EvalSymlinks form of its root. On macOS every temporary directory is
// reached through a symlinked /var, so a model quoting an absolute path it was
// handed would otherwise be told the workspace does not contain its own files.
func TestRootReachedThroughASymlinkAcceptsBothSpellings(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	link := filepath.Join(base, "link")
	mkdir(t, target)
	write(t, filepath.Join(target, "inside.txt"), "inside")
	symlink(t, target, link)

	ws, err := workspace.Open(link)
	if err != nil {
		t.Fatalf("opening the workspace through a symlink: %v", err)
	}
	defer ws.Close()

	// Asserted here rather than only in the fixture, where /tmp is a real
	// directory on Linux and the same comparison holds whether or not Open
	// resolves anything. Here link and target differ on every platform.
	if want := evalSymlinks(t, target); ws.Root() != want {
		t.Errorf("Root() = %q, want the symlink resolved to %q", ws.Root(), want)
	}

	for _, given := range []string{
		filepath.Join(link, "inside.txt"),
		filepath.Join(ws.Root(), "inside.txt"),
	} {
		got, err := ws.ReadFile(given)
		if err != nil {
			t.Errorf("ReadFile(%q) = %v, want it accepted", given, err)
			continue
		}
		if string(got) != "inside" {
			t.Errorf("ReadFile(%q) = %q, want %q", given, got, "inside")
		}
	}

	elsewhere := filepath.Join(base, "elsewhere.txt")
	write(t, elsewhere, "no")
	if _, err := ws.ReadFile(elsewhere); !errors.Is(err, workspace.ErrOutside) {
		t.Errorf("ReadFile(%q) = %v, want ErrOutside — accepting a second spelling of the root "+
			"must not widen what counts as inside it", elsewhere, err)
	}
}

func TestOpenRefusesAMissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nowhere")

	ws, err := workspace.Open(missing)
	if err == nil {
		ws.Close()
		t.Fatal("Open of a missing directory succeeded, want an error")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q does not name the directory it could not open", err)
	}
}

// assertOutside checks the refusal a caller can act on: the sentinel, the path
// they supplied, and the root they were confined to. The path matters most — a
// message naming the cleaned remainder tells a model nothing about what it asked
// for.
func assertOutside(t *testing.T, err error, given, root string) {
	t.Helper()

	if !errors.Is(err, workspace.ErrOutside) {
		t.Errorf("error %v does not match ErrOutside", err)
		return
	}

	var pathErr *workspace.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("error %v is not a *workspace.PathError, so nothing can read the path off it", err)
		return
	}
	if pathErr.Path != given {
		t.Errorf("PathError.Path = %q, want the path as supplied, %q", pathErr.Path, given)
	}
	if pathErr.Root != root {
		t.Errorf("PathError.Root = %q, want %q", pathErr.Root, root)
	}
	if pathErr.Op == "" {
		t.Error("PathError.Op is empty, so the message cannot say what was being attempted")
	}
	if msg := err.Error(); !strings.Contains(msg, given) || !strings.Contains(msg, root) {
		t.Errorf("message %q should name both the offending path %q and the workspace root %q",
			msg, given, root)
	}
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func evalSymlinks(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving %s: %v", dir, err)
	}
	return resolved
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// symlink creates one, or skips the test where the platform will not allow it.
// Windows needs developer mode or an elevated process, and CI runs the suite on
// Linux only — so the symlink cases are skipped there rather than silently
// reduced to something weaker.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating a symlink needs privileges on Windows: %v", err)
		}
		t.Fatalf("linking %s -> %s: %v", link, target, err)
	}
}
