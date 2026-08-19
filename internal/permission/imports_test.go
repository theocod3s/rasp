package permission_test

import (
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// allowed is every package the ladder's own sources may import. A grant is
// session-scoped and in-memory (design §7.7), and the cheapest way for that to stop
// being true is a well-meant "remember my answers" that reaches for os or
// encoding/json — a change that would look like a feature in review and would
// leave a file behind that answers for the user before rasp has drawn anything.
//
// Adding to this list is that decision, made deliberately.
var allowedImports = []string{
	"context",
	"errors",
	"fmt",
	"sync",
	"sync/atomic",
}

func TestGrantsHaveNowhereToPersistTo(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	fset := token.NewFileSet()
	inspected := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		inspected++

		for _, spec := range f.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: cannot read the import path %s: %v", name, spec.Path.Value, err)
			}
			if !slices.Contains(allowedImports, path) {
				t.Errorf("%s imports %q, which is not on this test's allowlist. Add it once you have "+
					"decided it cannot outlast the process — a grant that survives a restart is one "+
					"the user is never asked about again", name, path)
			}
		}
	}

	// Inspecting nothing is the quietest pass there is, and a renamed file — or
	// one moved into a subdirectory, which this walk does not follow — is all it
	// takes. The count is every non-test file the package has.
	if inspected < 3 {
		t.Fatalf("inspected %d non-test file(s); this package has three", inspected)
	}
}
