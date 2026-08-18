package builtin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/workspace"
)

// maxReadBytes is how much file content one call may return. At roughly four
// bytes to the token it lands near 32k tokens, which keeps a whole-file read
// under the tool-output budget compaction protects before it prunes (design
// §11); one read filling the context window is the thing the cap exists to stop.
const maxReadBytes = 128 << 10

// ctxCheckLines is how often the scan looks at cancellation. Skipping ahead to a
// large offset is unbounded work even though the output is not, and checking per
// line is a mutex acquisition for every line we are throwing away.
const ctxCheckLines = 4096

const readDescription = "Read a file from the workspace. Each line comes back prefixed with its " +
	"line number and a tab; the prefix is added by this tool and is not part of the file, so never " +
	"reproduce it in an edit or a write. Reads the whole file by default. Pass offset and limit to " +
	"take a line window instead, which is also how to read a file too large to return whole."

type readIn struct {
	Path   string `json:"path"             desc:"Path to the file, relative to the workspace root or absolute"`
	Offset int    `json:"offset,omitempty" desc:"First line to return, counting from 1. Omit to start at the top of the file"`
	Limit  int    `json:"limit,omitempty"  desc:"How many lines to return. Omit to read to the end of the file"`
}

// ReadDetails is the read tool's payload for the UI, which the model never sees.
type ReadDetails struct {
	Path  string // workspace-relative
	Start int    // line number of the first line returned
	Lines int
	Bytes int
}

// NewRead builds the read tool over ws, recording every successful read in reads
// so a later edit can tell a file this session has seen from one it has not.
func NewRead(ws *workspace.Workspace, reads *workspace.Tracker) tool.Tool {
	switch {
	case ws == nil:
		panic("builtin: read needs a workspace, which is the only route a file tool has to the filesystem")
	case reads == nil:
		panic("builtin: read needs a tracker, or nothing records the read that read-before-edit checks for")
	}
	return tool.New("read", readDescription, func(ctx context.Context, in readIn) (tool.Result, error) {
		return runRead(ctx, ws, reads, in)
	})
}

func runRead(ctx context.Context, ws *workspace.Workspace, reads *workspace.Tracker, in readIn) (tool.Result, error) {
	if in.Offset < 0 || in.Limit < 0 {
		return readFailed("read %q: offset and limit count lines, so neither can be negative (offset %d, limit %d)",
			in.Path, in.Offset, in.Limit), nil
	}

	rel, err := ws.Resolve(in.Path)
	if err != nil {
		return readFailed("%v", err), nil
	}
	f, err := ws.Open(rel)
	if err != nil {
		return readFailed("%v", err), nil
	}
	defer f.Close()

	// Stat the open handle before a byte is read, so the mtime the tracker keeps
	// belongs to the content going back to the model rather than to whatever the
	// path names once the scan has finished.
	info, err := f.Stat()
	if err != nil {
		return readFailed("read %q: %v", rel, err), nil
	}
	if info.IsDir() {
		return readFailed("read %q: it is a directory, not a file. Use ls to see what is inside it", rel), nil
	}

	start := max(in.Offset, 1)
	lines, scanned, kept, err := scanWindow(ctx, f, start, in.Limit)
	if err != nil {
		var overflow *overflowError
		switch {
		case errors.As(err, &overflow):
			return readTooLarge(rel, start, len(lines), overflow.line), nil
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			// The turn is being torn down, which is the tool failing to run rather
			// than an observation the model can act on (design §12).
			return tool.Result{}, err
		default:
			return readFailed("read %q: %v", rel, err), nil
		}
	}
	if len(lines) == 0 && in.Offset > 0 {
		return readFailed("read %q: offset %d starts past the end of the file, which has %d lines",
			rel, in.Offset, scanned), nil
	}

	reads.Record(rel, info.ModTime())

	content := numberLines(lines, start)
	if content == "" {
		// A tool result whose content is the empty string is refused on the wire by
		// providers that require a non-empty text block, so an empty file says so
		// in words instead.
		content = fmt.Sprintf("%s has no lines in it.", rel)
	}
	return tool.Result{
		Content: content,
		Title:   readTitle(rel, start, len(lines), in),
		Details: &ReadDetails{Path: rel, Start: start, Lines: len(lines), Bytes: kept},
	}, nil
}

// overflowError names the line at which the content passed maxReadBytes. It
// travels as an error so one scan can report it beside the lines it did collect,
// which is what lets the refusal name a window that would have fit.
type overflowError struct{ line int }

func (e *overflowError) Error() string {
	return fmt.Sprintf("line %d takes the output past %d bytes", e.line, maxReadBytes)
}

// scanWindow collects the lines from start, limit of them or to the end of the
// file when limit is zero, and reports how many lines it got through and how many
// bytes of file content it kept. It gives up the moment the kept content would
// pass maxReadBytes, so an enormous file costs a bounded read rather than all of
// it.
func scanWindow(ctx context.Context, r io.Reader, start, limit int) ([]string, int, int, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, maxReadBytes+1)

	var lines []string
	scanned, kept := 0, 0
	for sc.Scan() {
		scanned++
		if scanned%ctxCheckLines == 0 {
			if err := ctx.Err(); err != nil {
				return lines, scanned, kept, err
			}
		}
		if scanned < start {
			continue
		}

		line := sc.Text()
		if kept+len(line)+1 > maxReadBytes {
			return lines, scanned, kept, &overflowError{line: scanned}
		}
		kept += len(line) + 1
		lines = append(lines, line)

		if limit > 0 && len(lines) == limit {
			return lines, scanned, kept, nil
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			// Scan stops without counting the line it could not hold.
			return lines, scanned, kept, &overflowError{line: scanned + 1}
		}
		return lines, scanned, kept, err
	}
	return lines, scanned, kept, nil
}

func numberLines(lines []string, start int) string {
	if len(lines) == 0 {
		return ""
	}
	width := len(strconv.Itoa(start + len(lines) - 1))

	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%*d\t%s\n", width, start+i, line)
	}
	return b.String()
}

func readTooLarge(rel string, start, kept, at int) tool.Result {
	if kept == 0 {
		return readFailed("read %q: line %d is on its own larger than the %d bytes one read returns, so "+
			"no offset and limit can bring it back. The file is most likely minified or not text",
			rel, at, maxReadBytes)
	}
	return readFailed("read %q: returning it from line %d passes the %d bytes one read returns, at line %d. "+
		"Call read again with offset and limit for a window — lines %d to %d fit",
		rel, start, maxReadBytes, at, start, start+kept-1)
}

func readTitle(rel string, start, count int, in readIn) string {
	switch {
	case count == 0:
		return fmt.Sprintf("read %s (empty)", rel)
	case in.Offset == 0 && in.Limit == 0:
		return fmt.Sprintf("read %s (%d lines)", rel, count)
	}
	return fmt.Sprintf("read %s (lines %d-%d)", rel, start, start+count-1)
}

func readFailed(format string, a ...any) tool.Result {
	return tool.Result{IsError: true, Content: fmt.Sprintf(format, a...)}
}
