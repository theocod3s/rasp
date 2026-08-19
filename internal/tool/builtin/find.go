package builtin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/workspace"
)

// maxFindPaths is how many paths one call returns. The exact total comes back
// regardless, so a model holding a truncated list can narrow the pattern rather
// than guess whether it saw everything.
const maxFindPaths = 100

const findDescription = `Find files whose path matches a glob pattern, one workspace-relative path per line.

The pattern is matched against the whole path from the workspace root, so **/ is what makes it
match at any depth: **/*.go finds every Go file, internal/**/*_test.go only the test files under
internal. * and ? stop at a path separator, [abc] is a character class, and {a,b} is an
alternation.

Only files come back, never a directory and never a symlink. Files ignored by a .gitignore in the
searched tree are skipped. Results are capped, so narrow the pattern when the count comes back
large.`

type findInput struct {
	Pattern string `json:"pattern"        desc:"Glob pattern, matched against each file's whole path from the workspace root. Use **/ to match at any depth."`
	Path    string `json:"path,omitempty" desc:"Directory to search, relative to the workspace root or absolute. Defaults to the whole workspace. It only limits which subtree is walked; the pattern is still matched against the whole path from the workspace root."`
}

// FindDetails is the search's payload for the UI, which the model never sees.
type FindDetails struct {
	Pattern string
	Path    string   // the searched path, workspace-relative
	Paths   []string // workspace-relative, sorted, at most maxFindPaths of them
	Total   int      // every file matched, which may exceed len(Paths)
}

// NewFind builds the find tool over ws.
func NewFind(ws *workspace.Workspace) tool.Tool {
	if ws == nil {
		panic("builtin: find needs a workspace, which is the only route a file tool has to the filesystem")
	}
	return tool.New("find", findDescription, func(ctx context.Context, in findInput) (tool.Result, error) {
		return runFind(ctx, ws, in)
	})
}

func runFind(ctx context.Context, ws *workspace.Workspace, in findInput) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if in.Pattern == "" {
		return findFailed("find was called with no pattern to match paths against."), nil
	}
	// Validated up front rather than left to the first Match call, so a malformed
	// pattern is refused whether or not the walk ever reaches a file to try it on.
	if !doublestar.ValidatePattern(in.Pattern) {
		return findFailed("find %q: the pattern is malformed. Every [ needs a closing ], "+
			"every { a closing }, and a trailing \\ has nothing left to escape.", in.Pattern), nil
	}

	target := in.Path
	if target == "" {
		target = "."
	}
	// Stat before the walk: walkFiles skips what it cannot read rather than
	// failing, so without this a path that is not there comes back as "no files
	// match" — an answer about the workspace, to a question about a typo.
	if _, err := ws.Stat(target); err != nil {
		return findFailed("%v", err), nil
	}
	rel, err := ws.Resolve(target)
	if err != nil {
		return findFailed("%v", err), nil
	}

	c := &findCollector{}
	err = walkFiles(ctx, ws.FS(), rel, func(p string) error {
		switch ok, err := doublestar.Match(in.Pattern, p); {
		case err != nil:
			return err
		case ok:
			c.add(p)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// The turn is being torn down, which is the tool failing to run rather
			// than an observation the model can act on (design §12).
			return tool.Result{}, err
		}
		return findFailed("find %q: %v", in.Pattern, err), nil
	}
	return findResult(in.Pattern, rel, c), nil
}

// findCollector keeps the maxFindPaths lowest paths and counts the rest. The
// walk reaches a/b.txt before a.txt — a directory entry sorts before a longer
// sibling name, and its whole subtree then comes out first — so a cap applied
// as paths arrive would keep a different set than the one the listing is
// sorted into, chosen by the shape of the tree.
type findCollector struct {
	paths []string
	total int
}

func (c *findCollector) add(p string) {
	c.total++
	i, _ := slices.BinarySearch(c.paths, p)
	if i >= maxFindPaths {
		return
	}
	c.paths = slices.Insert(c.paths, i, p)
	if len(c.paths) > maxFindPaths {
		c.paths = c.paths[:maxFindPaths]
	}
}

func findResult(pattern, rel string, c *findCollector) tool.Result {
	details := &FindDetails{Pattern: pattern, Path: rel, Paths: c.paths, Total: c.total}
	if c.total == 0 {
		where := "the workspace"
		if rel != "." {
			where = rel
		}
		return tool.Result{
			Content: fmt.Sprintf("No files match %q in %s.", pattern, where),
			Title:   fmt.Sprintf("find %s (no files)", pattern),
			Details: details,
		}
	}

	var b strings.Builder
	for _, p := range c.paths {
		b.WriteString(p)
		b.WriteByte('\n')
	}
	if c.total > len(c.paths) {
		fmt.Fprintf(&b, "\nShowing the first %d of %d files, in path order. Narrow the pattern, "+
			"or pass path to search one subtree.\n", len(c.paths), c.total)
	}
	return tool.Result{
		Content: b.String(),
		Title:   fmt.Sprintf("find %s (%s)", pattern, findCount(c.total)),
		Details: details,
	}
}

func findCount(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

func findFailed(format string, a ...any) tool.Result {
	return tool.Result{IsError: true, Content: fmt.Sprintf(format, a...)}
}
