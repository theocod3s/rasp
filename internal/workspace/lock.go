package workspace

import (
	"path/filepath"
	"sync"
)

// LockFile takes the mutation lock for name and returns the function that
// releases it: same-file mutations serialize, different files stay parallel
// (design §6 rule 6). Callers hold it across a whole read-modify-write, not only
// the write. Every spelling of one file takes one lock — "a.go", "./a.go", the
// absolute path, and a symlink inside the workspace pointing at it.
func (w *Workspace) LockFile(name string) (unlock func(), err error) {
	key, err := w.lockKey(name)
	if err != nil {
		return nil, err
	}

	entry, _ := w.locks.LoadOrStore(key, &sync.Mutex{})
	mu := entry.(*sync.Mutex)
	mu.Lock()

	// Not mu.Unlock itself: called a second time that releases whoever holds the
	// file next rather than panicking — the corruption this lock exists to
	// prevent, reached through the lock itself.
	return sync.OnceFunc(mu.Unlock), nil
}

// lockKey resolves name's symlinks, which is what makes every spelling of a file
// one key; keyed on anything the caller supplied, the mutex looks right and
// guards nothing. A file that does not exist yet — the one a write is creating —
// has no realpath, so the cleaned absolute path stands in, and because the root
// is already canonical that is what EvalSymlinks returns once the file does
// exist.
func (w *Workspace) lockKey(name string) (string, error) {
	abs, err := w.Abs(name)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs, nil
	}
	return real, nil
}
