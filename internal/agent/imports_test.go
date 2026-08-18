package agent_test

import (
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// allowed is every package the loop's own sources may import. An allowlist
// rather than a list of the things it must not reach: "no terminal code, no
// HTTP, no filesystem syscall, and no knowledge of modes" is four properties,
// and a denylist of bubbletea, net/http and os only ever names the imports
// somebody already thought of.
//
// Growing this list is a layering decision, not a formality — the loop is meant
// to meet its services through the interfaces in llm and tool (design §1, §2).
var allowed = []string{
	"context",
	"encoding/json",
	"errors",
	"fmt",
	"slices",
	"sync",
	"github.com/theocod3s/rasp/internal/llm",
	"github.com/theocod3s/rasp/internal/tool",
}

// TestTheLoopReachesNothingItShouldNot holds design §2's row for this package.
// The frontends consume agent.Event and nothing else, which stops being true the
// moment the loop can draw something itself; the same seam is what makes a
// headless run free.
func TestTheLoopReachesNothingItShouldNot(t *testing.T) {
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
			if !slices.Contains(allowed, path) {
				t.Errorf("%s imports %q, which is not on this test's allowlist. Add it once you have "+
					"decided it is not terminal code, not HTTP, not a filesystem syscall and not a "+
					"mode the loop would then be branching on", name, path)
			}
		}
	}

	// Inspecting nothing is the quietest pass there is, and one rename of the
	// package directory is all it takes.
	if inspected < 3 {
		t.Fatalf("inspected %d non-test file(s) in this package; the loop's own sources are more than that", inspected)
	}
}
