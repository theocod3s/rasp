package builtin

import (
	"context"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/workspace"
)

// maxLsEntries bounds one listing. prd §6.2 asks tool output to keep its head
// and its tail; a sorted listing has neither the command echo of the one nor the
// error of the other, so this keeps the head and says how many names it dropped.
const maxLsEntries = 1000

const lsDescription = "List the contents of one directory in the workspace. It does not descend: a " +
	"subdirectory comes back as a name, not as its contents. Each entry is on its own line as its " +
	"name, a tab, and then its size in bytes if it is a file or what it is otherwise. Directories " +
	"are listed first, then everything else, each in name order. Omit path to list the workspace root."

type lsIn struct {
	Path string `json:"path,omitempty" desc:"Directory to list, relative to the workspace root or absolute. Omit it to list the workspace root."`
}

// LsKind is what one entry is, in the word the listing gives the model.
type LsKind string

const (
	LsDir     LsKind = "dir"
	LsFile    LsKind = "file"
	LsSymlink LsKind = "symlink"
	LsOther   LsKind = "other"
)

// LsEntry is one entry of a listing. Size is bytes for an LsFile and -1
// everywhere else, including a file whose size could not be read.
type LsEntry struct {
	Name string
	Kind LsKind
	Size int64
}

// LsDetails is the ls tool's payload for the UI, which the model never sees.
type LsDetails struct {
	Path    string // workspace-relative; "." is the root
	Entries []LsEntry
	Total   int // before the cap, so the UI can say what it is not showing
}

// NewLs builds the ls tool over ws.
func NewLs(ws *workspace.Workspace) tool.Tool {
	if ws == nil {
		panic("builtin: ls needs a workspace, which is the only route a file tool has to the filesystem")
	}
	return tool.New("ls", lsDescription, func(ctx context.Context, in lsIn) (tool.Result, error) {
		return runLs(ctx, ws, in)
	})
}

func runLs(ctx context.Context, ws *workspace.Workspace, in lsIn) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}

	name := in.Path
	if strings.TrimSpace(name) == "" {
		name = "."
	}
	rel, err := ws.Resolve(name)
	if err != nil {
		return lsFailed("%v", err), nil
	}

	// Stat before ReadDir so a path that is a file is refused in words. Reading
	// a file as a directory fails at the syscall with a message that differs per
	// platform and points the model at nothing.
	info, err := ws.Stat(rel)
	if err != nil {
		return lsFailed("%v", err), nil
	}
	if !info.IsDir() {
		return lsFailed("list %q: it is a file, not a directory. Use read to see what is in it", rel), nil
	}

	entries, err := ws.ReadDir(rel)
	if err != nil {
		return lsFailed("%v", err), nil
	}

	list := make([]LsEntry, 0, len(entries))
	for _, e := range entries {
		list = append(list, lsEntryOf(e))
	}
	// os hands back the directory in whatever order it is stored in, which is
	// neither sorted nor stable across filesystems.
	slices.SortFunc(list, lsOrder)

	total := len(list)
	if total > maxLsEntries {
		list = list[:maxLsEntries]
	}

	return tool.Result{
		Content: renderLs(rel, list, total),
		Title:   fmt.Sprintf("ls %s (%s)", rel, lsCount(total)),
		Details: &LsDetails{Path: rel, Entries: list, Total: total},
	}, nil
}

// lsOrder puts directories first and sorts each group by name. Grouping is for
// the reader rather than the machine: what follows a listing is usually a choice
// of where to descend, and the answers are then together at the top.
func lsOrder(a, b LsEntry) int {
	if (a.Kind == LsDir) != (b.Kind == LsDir) {
		if a.Kind == LsDir {
			return -1
		}
		return 1
	}
	return strings.Compare(a.Name, b.Name)
}

func lsEntryOf(e fs.DirEntry) LsEntry {
	entry := LsEntry{Name: e.Name(), Kind: lsKindOf(e.Type()), Size: -1}
	if entry.Kind != LsFile {
		return entry
	}
	info, err := e.Info()
	if err != nil {
		// The entry went away between reading the directory and asking about it.
		// A listing missing one size is worth more than no listing at all.
		return entry
	}
	entry.Size = info.Size()
	return entry
}

func lsKindOf(mode fs.FileMode) LsKind {
	switch {
	case mode.IsDir():
		return LsDir
	case mode&fs.ModeSymlink != 0:
		// Reported as a link rather than as what it points at: following it is
		// the workspace's decision to make, and it refuses one leaving the root.
		return LsSymlink
	case mode.IsRegular():
		return LsFile
	}
	return LsOther
}

func renderLs(rel string, entries []LsEntry, total int) string {
	where := rel
	if rel == "." {
		where = "The workspace root"
	}
	if total == 0 {
		// Never the empty string: a tool_result carrying no content reads as a
		// tool that did nothing, rather than a directory with nothing in it.
		return fmt.Sprintf("%s has nothing in it.", where)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s has %s:\n", where, lsCount(total))
	for _, e := range entries {
		fmt.Fprintf(&b, "%s\t%s\n", lsName(e.Name), lsWhat(e))
	}
	if omitted := total - len(entries); omitted > 0 {
		fmt.Fprintf(&b, "... and %s not listed; one call returns at most %d.\n", lsCount(omitted), maxLsEntries)
	}
	return b.String()
}

func lsWhat(e LsEntry) string {
	switch {
	case e.Kind != LsFile:
		// A directory's own size is the size of its entry table, which says
		// nothing about what is inside it, so anything that is not a file says
		// what it is where a file says how big it is.
		return string(e.Kind)
	case e.Size < 0:
		return "file, size unknown"
	}
	// Exact bytes rather than a rounded 1.2 KB. What a size is for here is
	// deciding whether a file will come back whole from one read, and that is a
	// comparison against a byte budget a rounded figure cannot be held up to.
	return fmt.Sprintf("%d B", e.Size)
}

// lsName quotes a name carrying a control character. The listing is one entry
// per line, so a name containing a newline would otherwise read as two entries,
// one of which does not exist.
func lsName(name string) string {
	if strings.ContainsFunc(name, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return strconv.Quote(name)
	}
	return name
}

func lsCount(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}

func lsFailed(format string, a ...any) tool.Result {
	return tool.Result{IsError: true, Content: fmt.Sprintf(format, a...)}
}
