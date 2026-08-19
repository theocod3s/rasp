package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Workspace is the only route from a tool to the filesystem. Its methods take a
// path as the model supplied it — relative to the workspace root, or absolute —
// and every one of them refuses a path that resolves outside.
type Workspace struct {
	root *os.Root
	dir  string

	// given is dir before EvalSymlinks, kept only when the two differ, so an
	// absolute path spelled the way the user spelled it still resolves. macOS
	// hands every process /tmp and /var as symlinks, which makes this the normal
	// case there rather than an exotic one.
	given string

	// locks maps a resolved path to the mutex mutations of it serialize on.
	// Entries are never removed: one mutex per file a session mutated is not
	// worth the refcounting it would take to reclaim.
	locks sync.Map
}

// Open confines a Workspace to dir, which must exist.
func Open(dir string) (*Workspace, error) {
	given, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("workspace root %s: %w", dir, err)
	}
	// Resolving the root's own symlinks is what makes Root() comparable with a
	// realpath, which the per-file mutation mutex keys on (design §6 rule 6).
	canonical, err := filepath.EvalSymlinks(given)
	if err != nil {
		return nil, fmt.Errorf("workspace root %s: %w", dir, err)
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, fmt.Errorf("workspace root %s: %w", dir, err)
	}

	w := &Workspace{root: root, dir: canonical}
	if given != canonical {
		w.given = given
	}
	return w, nil
}

// Close releases the root handle. A Workspace is unusable afterwards.
func (w *Workspace) Close() error { return w.root.Close() }

// Root is the absolute workspace root with symlinks resolved.
func (w *Workspace) Root() string { return w.dir }

// Resolve turns a caller-supplied path into the cleaned, slash-separated,
// root-relative form the rest of this package operates on, or reports that it
// lies outside the workspace.
func (w *Workspace) Resolve(name string) (string, error) { return w.resolve("resolve", name) }

// Abs is name as an absolute path. It resolves through the same confinement, so
// it never names a location outside the workspace.
func (w *Workspace) Abs(name string) (string, error) {
	rel, err := w.resolve("resolve", name)
	if err != nil {
		return "", err
	}
	return filepath.Join(w.dir, filepath.FromSlash(rel)), nil
}

// FS exposes the root for walking, which grep and find need and os.Root has no
// method for. Confinement holds through it: a symlink leaving the root fails to
// open here too. Names given to it must satisfy fs.ValidPath, so pass Resolve's
// output rather than the model's.
func (w *Workspace) FS() fs.FS { return w.root.FS() }

func (w *Workspace) ReadFile(name string) ([]byte, error) {
	return through(w, "read", name, w.root.ReadFile)
}

func (w *Workspace) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return act(w, "write", name, func(rel string) error {
		return w.root.WriteFile(rel, data, perm)
	})
}

func (w *Workspace) Open(name string) (*os.File, error) {
	return through(w, "open", name, w.root.Open)
}

func (w *Workspace) OpenFile(name string, flag int, perm fs.FileMode) (*os.File, error) {
	return through(w, "open", name, func(rel string) (*os.File, error) {
		return w.root.OpenFile(rel, flag, perm)
	})
}

func (w *Workspace) Stat(name string) (fs.FileInfo, error) {
	return through(w, "stat", name, w.root.Stat)
}

// ReadDir reads one directory. os.Root has no ReadDir, so this opens the
// directory and drains it.
func (w *Workspace) ReadDir(name string) ([]fs.DirEntry, error) {
	return through(w, "list", name, func(rel string) ([]fs.DirEntry, error) {
		d, err := w.root.Open(rel)
		if err != nil {
			return nil, err
		}
		defer d.Close()
		return d.ReadDir(-1)
	})
}

func (w *Workspace) MkdirAll(name string, perm fs.FileMode) error {
	return act(w, "mkdir", name, func(rel string) error { return w.root.MkdirAll(rel, perm) })
}

func (w *Workspace) Remove(name string) error {
	return act(w, "remove", name, w.root.Remove)
}

func (w *Workspace) Rename(oldname, newname string) error {
	oldRel, err := w.resolve("rename", oldname)
	if err != nil {
		return err
	}
	newRel, err := w.resolve("rename", newname)
	if err != nil {
		return err
	}
	if err := w.root.Rename(oldRel, newRel); err != nil {
		return w.fail("rename", w.culprit(err, oldRel, oldname, newname), err)
	}
	return nil
}

// culprit picks which side of a rename to name. os reports a failed rename as a
// LinkError over both paths without saying which one it objected to, so an escape
// is attributed by asking about the source on its own: a path that leaves the
// root fails the same way by itself, and if it does not, the destination did.
func (w *Workspace) culprit(err error, oldRel, oldname, newname string) string {
	if !isEscape(err) {
		return oldname
	}
	if _, err := w.root.Stat(oldRel); isEscape(err) {
		return oldname
	}
	return newname
}

func (w *Workspace) resolve(op, name string) (string, error) {
	if name == "" {
		return "", &PathError{Op: op, Path: name, Root: w.dir, Err: errEmptyPath}
	}

	rel := name
	if filepath.IsAbs(name) {
		// os.Root refuses every absolute path, including one that points inside
		// the root, so translating them is this package's job rather than a
		// convenience. Models produce absolute paths constantly — out of compiler
		// output, grep hits and the user's own prose — and the alternative is
		// refusing a path that is demonstrably inside the workspace.
		var ok bool
		if rel, ok = w.relative(name); !ok {
			return "", &PathError{Op: op, Path: name, Root: w.dir, Err: ErrOutside}
		}
	}

	// Lexical cleaning only decides paths whose escape is visible in the string.
	// A symlink out of the root is invisible here and gets refused by os.Root at
	// the point of use — the runtime half of the confinement guarantee (design §2).
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", &PathError{Op: op, Path: name, Root: w.dir, Err: ErrOutside}
	}
	return rel, nil
}

func (w *Workspace) relative(abs string) (string, bool) {
	for _, root := range []string{w.dir, w.given} {
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return rel, true
	}
	return "", false
}

func (w *Workspace) fail(op, name string, err error) error {
	inner := innermost(err)
	if isEscape(inner) {
		inner = ErrOutside
	}
	return &PathError{Op: op, Path: name, Root: w.dir, Err: inner}
}

// innermost strips the wrapper os put the failure in — *fs.PathError, or
// *os.LinkError for a rename. Both carry the root-relative remainder rather than
// the path the caller supplied, which PathError names instead. Unwrapping to the
// end rather than matching either type is what keeps a third wrapper from
// silently reading as an ordinary error.
func innermost(err error) error {
	for next := errors.Unwrap(err); next != nil; next = errors.Unwrap(err) {
		err = next
	}
	return err
}

func isEscape(err error) bool { return err != nil && innermost(err).Error() == escapeRefusal }

func through[T any](w *Workspace, op, name string, fn func(rel string) (T, error)) (T, error) {
	var zero T
	rel, err := w.resolve(op, name)
	if err != nil {
		return zero, err
	}
	v, err := fn(rel)
	if err != nil {
		return zero, w.fail(op, name, err)
	}
	return v, nil
}

func act(w *Workspace, op, name string, fn func(rel string) error) error {
	rel, err := w.resolve(op, name)
	if err != nil {
		return err
	}
	if err := fn(rel); err != nil {
		return w.fail(op, name, err)
	}
	return nil
}
