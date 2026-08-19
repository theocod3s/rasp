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
	MkdirAll(name string, perm fs.FileMode) error
	OpenFile(name string, flag int, perm fs.FileMode) (*os.File, error)
	Rename(oldname, newname string) error
	Remove(name string) error
}

// WriteDetails is the UI's payload for a completed write.
type WriteDetails struct {
	Path    string
	Bytes   int
	Created bool
}

type writeInput struct {
	Path string `json:"path" desc:"Path to the file, absolute or relative to the workspace root. Parent directories are created."`

	// A pointer separates an absent key from an empty string. Both are legal
	// JSON for a string field, and reading a missing content as "" would empty
	// the file the model named while reporting success.
	Content *string `json:"content" desc:"The file's entire new contents. An empty string writes an empty file."`
}

const writeDescription = "Write a file, creating any parent directories that do not exist and replacing the " +
	"file if it does. content is the file's entire new text, so prefer edit when changing part of a file. " +
	"The replacement is atomic: anything reading the path sees either the whole old file or the whole new one."

// NewWrite returns the write tool. It builds the new contents in a temporary
// file alongside the destination and renames it into place, so a write that
// fails partway leaves the original file as it was. reads is the session's
// read-before-edit tracker: overwriting a file this session has not read
// through it, or has read at a since-superseded mtime, is refused (prd §6.6).
// A path that does not exist yet is a creation, not an overwrite, and needs no
// prior read.
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

	dir := path.Dir(rel)
	if err := w.ws.MkdirAll(dir, 0o777); err != nil {
		return writeRefused(err.Error()), nil
	}

	data := []byte(*in.Content)
	if err := w.replace(dir, rel, data, perm, created); err != nil {
		// The inner error names the temporary file, which is not a path the
		// model asked about.
		return writeRefused(fmt.Sprintf("Cannot write %s: %v", rel, err)), nil
	}
	recordRead(w.reads, rel, w.ws.Stat)

	summary := fmt.Sprintf("Replaced %s", rel)
	if created {
		summary = fmt.Sprintf("Created %s", rel)
	}
	return tool.Result{
		Content: fmt.Sprintf("%s (%d bytes).", summary, len(data)),
		Title:   summary,
		Details: &WriteDetails{Path: rel, Bytes: len(data), Created: created},
	}, nil
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
