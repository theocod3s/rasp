package builtin

import (
	"fmt"
	"io/fs"
	"time"

	"github.com/theocod3s/rasp/internal/workspace"
)

// refuseUnread reports why path may not be mutated for action as it stands, or
// nil to proceed. modTime must come from a stat taken inside the caller's file
// lock, so the check is about the bytes the write that follows will touch.
//
// Any mismatch against the recorded read refuses, not only a newer mtime — a
// clock moved back or a restored file is just as unseen by this session as one
// that changed forward (design §5 step 17: the tracker confirms the mtime "is
// unchanged").
func refuseUnread(reads *workspace.Tracker, path string, modTime time.Time, action string) error {
	last, ok := reads.LastRead(path)
	switch {
	case !ok:
		return fmt.Errorf("%s has not been read this session. Read it before %s.", path, action)
	case !last.Equal(modTime):
		return fmt.Errorf("%s has changed on disk since this session last read it. Read it again "+
			"before %s.", path, action)
	}
	return nil
}

// recordRead re-stats path after a mutation and records it as freshly read, so
// the session may mutate it again without reading it back — the write already
// told it what path now holds. A failing stat is left unrecorded, not reported:
// the mutation already succeeded, and the worst an unrecorded read costs is a
// refusal asking for one.
func recordRead(reads *workspace.Tracker, path string, stat func(string) (fs.FileInfo, error)) {
	if info, err := stat(path); err == nil {
		reads.Record(path, info.ModTime())
	}
}
