package builtin

import (
	"fmt"

	udiff "github.com/aymanbagabas/go-udiff"

	"github.com/theocod3s/rasp/internal/tool"
)

// diffDetails renders the diff the UI draws, for every tool here that changes a
// file. Its own file rather than edit's, because write builds one too and a
// reader of write has no way to find a helper filed under another tool.
//
// udiff.Unified is the obvious call and the wrong one: it turns the error
// handled here into log.Fatalf, which would end the process over one tool call
// and print to a stdout the TUI owns.
//
// The two callers answer that error differently, deliberately. write drops the
// payload and writes anyway, because its diff is decoration. edit refuses,
// because its counts go into the Content and Title the model reads: a diff it
// cannot build is a result it cannot describe.
func diffDetails(path, before, after string, fuzzy bool) (*tool.DiffDetails, error) {
	u, err := udiff.ToUnifiedDiff("a/"+path, "b/"+path, before, udiff.Lines(before, after), udiff.DefaultContextLines)
	if err != nil {
		return nil, fmt.Errorf("diffing %s: %w", path, err)
	}

	details := &tool.DiffDetails{Path: path, Unified: u.String(), Fuzzy: fuzzy}
	for _, hunk := range u.Hunks {
		for _, line := range hunk.Lines {
			switch line.Kind {
			case udiff.Insert:
				details.Additions++
			case udiff.Delete:
				details.Deletions++
			}
		}
	}
	return details, nil
}
