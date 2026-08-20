package builtin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path"

	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/workspace"
)

// writeFS is the part of the workspace a write reaches the filesystem through.
// It is an interface rather than *workspace.Workspace so a test can fail one
// call and check what the failure left behind.
type writeFS interface {
	Resolve(name string) (string, error)
	LockFile(name string) (func(), error)
	Stat(name string) (fs.FileInfo, error)
	ReadFile(name string) ([]byte, error)
	MkdirAll(name string, perm fs.FileMode) error
	OpenFile(name string, flag int, perm fs.FileMode) (*os.File, error)
	Rename(oldname, newname string) error
	Remove(name string) error
}

type writeInput struct {
	Path string `json:"path" desc:"Path to the file, absolute or relative to the workspace root. Parent directories are created."`

	// A pointer separates an absent key from an empty string. Both are legal
	// JSON for a string field, and reading a missing content as "" would empty
	// the file the model named while reporting success.
	Content *string `json:"content" desc:"The file's entire new contents. An empty string writes an empty file."`
}

// maxDiffBytes is the largest file write will read back to diff. The read is
// for the card and nothing else, so past this the write still happens and the
// card says what it wrote — which beats holding a diff of a generated file for
// the session and drawing more rows than a terminal has.
const maxDiffBytes = 256 << 10

const writeDescription = "Write a file, creating any parent directories that do not exist and replacing the " +
	"file if it does. content is the file's entire new text, so prefer edit when changing part of a file. " +
	"The replacement is atomic: anything reading the path sees either the whole old file or the whole new one."

// NewWrite returns the write tool. It builds the new contents in a temporary
// file alongside the destination and renames it into place, so a write that
// fails partway leaves the original file as it was. reads is the session's
// read-before-edit tracker: overwriting a file this session has not read
// through it, or has read at a since-superseded mtime, is refused. A path that
// does not exist yet is a creation, not an overwrite, and needs no prior read.
func NewWrite(ws writeFS, reads *workspace.Tracker) tool.Tool {
	switch {
	case ws == nil:
		panic("builtin: write needs a workspace, which is the only route a file tool has to the filesystem")
	case reads == nil:
		panic("builtin: write needs a tracker, or it cannot tell a file this session read from one it has not")
	}
	w := &writeTool{ws: ws, reads: reads}
	return tool.New("write", writeDescription, w.run)
}

type writeTool struct {
	ws    writeFS
	reads *workspace.Tracker
}

func (w *writeTool) run(ctx context.Context, in writeInput) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if in.Content == nil {
		return writeRefused("The call has no content. Send the file's entire new text as content, " +
			"or an empty string to write an empty file."), nil
	}

	rel, err := w.ws.Resolve(in.Path)
	if err != nil {
		return writeRefused(err.Error()), nil
	}

	// Taken before the stat, not around the rename: the mode and the created flag
	// are read off the file this call is about to replace, and reading them
	// outside the lock reports creating a file that already existed, with the
	// mode of one that did not.
	unlock, err := w.ws.LockFile(rel)
	if err != nil {
		return writeRefused(err.Error()), nil
	}
	defer unlock()

	info, err := w.ws.Stat(rel)
	switch {
	case err == nil && info.IsDir():
		return writeRefused(fmt.Sprintf("write %q: it is a directory, not a file", in.Path)), nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return writeRefused(err.Error()), nil
	}
	created := err != nil
	if !created {
		if err := refuseUnread(w.reads, rel, info.ModTime(), "overwriting it"); err != nil {
			return writeRefused(err.Error()), nil
		}
	}

	// os masks the mode of a file it creates by the process umask, so an
	// overwrite needs the chmod as well to land on the mode it found. Creating
	// the temporary file with that mode rather than a default keeps it from
	// being readable more widely than the file it is about to replace.
	perm := fs.FileMode(0o666)
	if !created {
		perm = info.Mode().Perm()
	}

	// A diff is worth taking only when both sides of it are: the file being
	// replaced, and the text replacing it. Bounding one alone is not a bound —
	// a creation has no old side at all, so it would always be diffed however
	// much was written.
	diffable := len(*in.Content) <= maxDiffBytes && (created || info.Size() <= maxDiffBytes)

	// The bytes about to be replaced, read under the same lock as the rename.
	// The stat above and this are two moments, and the lock is rasp's own — a
	// checkout or an editor can take the file away between them or put one
	// there — so this is the later look and the one that decides.
	//
	// When the diff is not being taken there is nothing to read, but the later
	// look still has to happen: a stat costs nothing and keeps the race handling
	// below from switching itself off on file size.
	var (
		before  []byte
		readErr error
	)
	if diffable {
		before, readErr = w.ws.ReadFile(rel)
	} else {
		_, readErr = w.ws.Stat(rel)
	}

	// Not-there is the only answer meaning the path is empty; a mode that
	// forbids reading still means a file is there.
	existed := !errors.Is(readErr, fs.ErrNotExist)
	arrived := created && existed
	if !created && !existed {
		// created outlives the wording: it is whether a mode was read to
		// preserve, and a file that left leaves none.
		created, perm = true, 0o666
	}

	// A file that arrived is one this session never read, which is what the
	// tracker refuses. Its guard was skipped because the stat found nothing.
	//
	// Gone again by now is not a refusal: there is nothing left to protect, and
	// treating a third turn of the same race as a failure would refuse a write
	// that is once more an ordinary creation.
	if arrived {
		switch info, err := w.ws.Stat(rel); {
		case errors.Is(err, fs.ErrNotExist):
			// Gone again, so this is a creation once more — and has to be reported
			// as one, or the card claims to have replaced an empty path and the
			// diff shows lines nothing on disk had.
			existed = false
		case err != nil:
			return writeRefused(err.Error()), nil
		default:
			if err := refuseUnread(w.reads, rel, info.ModTime(), "overwriting it"); err != nil {
				return writeRefused(err.Error()), nil
			}
		}
	}

	data := []byte(*in.Content)

	// A payload for the UI decides how the write is drawn, never whether it
	// happens — replacing a file needs its directory, not the file, so one
	// readable only by root is still replaceable. Without the old bytes there is
	// no diff at all rather than one against nothing, which would claim every
	// line is new. Built before the write, so a failure here is not a file
	// already changed and a caller told the tool could not run.
	var details *tool.DiffDetails
	if diffable && (readErr == nil || errors.Is(readErr, fs.ErrNotExist)) {
		// Failing to build one is the same as not having the bytes to build it
		// from, and goes the same way: no Details, and the write still happens.
		details, _ = diffDetails(rel, string(before), *in.Content, false)
	}

	dir := path.Dir(rel)
	if err := w.ws.MkdirAll(dir, 0o777); err != nil {
		return writeRefused(err.Error()), nil
	}

	if err := w.replace(dir, rel, data, perm, created); err != nil {
		// The inner error names the temporary file, which is not a path the
		// model asked about.
		return writeRefused(fmt.Sprintf("Cannot write %s: %v", rel, err)), nil
	}
	recordRead(w.reads, rel, w.ws.Stat)

	summary := fmt.Sprintf("Created %s", rel)
	if existed {
		summary = fmt.Sprintf("Replaced %s", rel)
	}
	result := tool.Result{
		// The diff stays in Details: it is what the UI draws, and the model just
		// wrote the file it is a diff of.
		Content: fmt.Sprintf("%s (%d bytes).", summary, len(data)),
		Title:   summary,
	}
	// Assigned only when there is one, because a nil *DiffDetails in an any is
	// not a nil any: the UI's type assertion would succeed and hand it a pointer
	// to dereference.
	if details != nil {
		result.Details = details
	}
	return result, nil
}

func (w *writeTool) replace(dir, rel string, data []byte, perm fs.FileMode, created bool) error {
	name, f, err := w.createTemp(dir, perm)
	if err != nil {
		return err
	}

	renamed := false
	defer func() {
		if renamed {
			return
		}
		_ = f.Close() // already closed on the later failure paths; the removal is the point
		_ = w.ws.Remove(name)
	}()

	if _, err := f.Write(data); err != nil {
		return err
	}
	if !created {
		if err := f.Chmod(perm); err != nil {
			return err
		}
	}
	// Closing before the rename rather than deferring it: a write buffered by
	// the kernel can fail here, and reporting that after the file is in place
	// would report it too late.
	if err := f.Close(); err != nil {
		return err
	}
	// A rename does not follow a symlink at the destination, so a path inside the
	// workspace that is one ends up a regular file. Noticing would take an Lstat
	// the workspace does not expose.
	if err := w.ws.Rename(name, rel); err != nil {
		return err
	}
	renamed = true
	return nil
}

// createTemp opens a new file in dir, which has to be the destination's own
// directory: a rename across filesystems fails outright, so a temporary file
// kept anywhere tidier would break on any workspace that is its own mount.
func (w *writeTool) createTemp(dir string, perm fs.FileMode) (string, *os.File, error) {
	const attempts = 10
	for range attempts {
		name := path.Join(dir, fmt.Sprintf(".rasp-write-%08x.tmp", rand.Uint32()))
		f, err := w.ws.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		switch {
		case err == nil:
			return name, f, nil
		case !errors.Is(err, fs.ErrExist):
			return "", nil, err
		}
	}
	return "", nil, fmt.Errorf("no free temporary file name in %s after %d attempts", dir, attempts)
}

func writeRefused(content string) tool.Result {
	return tool.Result{IsError: true, Content: content}
}
