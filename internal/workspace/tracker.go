package workspace

import (
	"sync"
	"time"
)

// Tracker remembers which files this session has read and the mtime each carried
// when it was read. That pair is the evidence an edit needs before it may
// overwrite anything: no entry means the model never saw the contents, and a
// newer mtime means somebody changed them since it did (design §5 step 17).
//
// Paths are keyed as Resolve returns them, so every spelling of one path inside
// the workspace agrees. Two names for the same file through a symlink do not, and
// that disagreement fails in the safe direction — the read goes unfound and an
// edit refuses, rather than a stale read passing for a fresh one.
type Tracker struct {
	mu    sync.Mutex
	reads map[string]time.Time
}

func NewTracker() *Tracker { return &Tracker{reads: map[string]time.Time{}} }

// Record notes that path held modTime when it was read. Callers stat before
// reading, never after: a file rewritten mid-read then records the older mtime
// and reads as stale, where the newer one would pass off a read of content nobody
// saw as a current one.
func (t *Tracker) Record(path string, modTime time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reads[path] = modTime
}

// LastRead is the mtime path carried when this session last read it.
func (t *Tracker) LastRead(path string) (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	modTime, ok := t.reads[path]
	return modTime, ok
}
