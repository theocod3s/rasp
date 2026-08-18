package workspace

import (
	"errors"
	"fmt"
)

// ErrOutside reports a path that resolved outside the workspace — by ../
// arithmetic, by being an absolute path somewhere else, or by traversing a
// symlink whose target leaves the root.
var ErrOutside = errors.New("outside the workspace")

// errEmptyPath is separate from ErrOutside because an empty path is a malformed
// request rather than an escape attempt, and telling the model otherwise sends it
// looking for a confinement problem it does not have.
var errEmptyPath = errors.New("empty path")

// escapeRefusal is the message os.Root returns for a path leaving the root. os
// keeps that error value unexported, so recognising it means matching its text —
// TestSymlinkOutOfTheWorkspaceIsRejected provokes the real refusal, so a Go
// release that rewords this fails there rather than silently downgrading every
// escape to a pass-through error.
const escapeRefusal = "path escapes from parent"

// PathError is every refusal this package makes. It names the path the caller
// supplied rather than the root-relative remainder os.Root saw: a model that
// asked for ../../../etc/passwd needs to be told about that path, not about
// whatever survived cleaning.
type PathError struct {
	Op   string // what the caller was doing: "read", "write", "list"
	Path string // as supplied, before cleaning
	Root string // the absolute workspace root, symlinks resolved
	Err  error
}

func (e *PathError) Error() string {
	if errors.Is(e.Err, ErrOutside) {
		return fmt.Sprintf("%s %q: outside the workspace %s", e.Op, e.Path, e.Root)
	}
	return fmt.Sprintf("%s %q: %v", e.Op, e.Path, e.Err)
}

func (e *PathError) Unwrap() error { return e.Err }
