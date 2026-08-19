package permission_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// allowedImports is every package the ladder's own sources may import. A grant
// is session-scoped and in-memory (design §7.7), and the cheapest way for that
// to stop being true is a well-meant "remember my answers" that reaches for os
// or encoding/json — a change that would look like a feature in review and
// would leave a file behind that answers for the user before rasp has drawn
// anything.
//
// Adding to this list is that decision, made deliberately.
var allowedImports = []string{
	"context",
	"errors",
	"fmt",
	"sync",
	"sync/atomic",
}

// minSources is what this package holds today. The walk below covers
// subdirectories as well, so the floor only has to be low enough not to need
// raising with every new file and high enough that a package emptied, renamed
// or moved out from under the test fails instead of passing on nothing.
const minSources = 3

func TestGrantsHaveNowhereToPersistTo(t *testing.T) {
	fset := token.NewFileSet()
	inspected := 0

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			return nil
		case !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
			return nil
		}

		f, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		inspected++

		for _, spec := range f.Imports {
			imported, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			if !slices.Contains(allowedImports, imported) {
				t.Errorf("%s imports %q, which is not on this test's allowlist. Add it once you have "+
					"decided it cannot outlast the process — a grant that survives a restart is one "+
					"the user is never asked about again", path, imported)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the package: %v", err)
	}

	// Inspecting nothing is the quietest pass there is, and a renamed file is all
	// it takes.
	if inspected < minSources {
		t.Fatalf("inspected %d non-test file(s), fewer than the %d this package is known to have",
			inspected, minSources)
	}
}
