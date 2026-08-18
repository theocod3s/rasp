package fake_test

import (
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// allowed is every package the fake's own sources may import. An allowlist rather
// than a list of the things it must not reach: "no network" is the property that
// matters, and a denylist of net, net/http and the rest only ever names the
// imports someone already thought of.
//
// The one entry outside the standard library is internal/llm, which design §2
// gives no network either.
var allowed = []string{
	"context",
	"encoding/json",
	"fmt",
	"slices",
	"sync",
	"github.com/theocod3s/rasp/internal/llm",
}

// TestNothingHereCanReachTheNetwork holds the doc.go promise the rest of the
// suite cannot see: a scripted provider is a pure function of its script, so a
// loop test running against it costs nothing and cannot flake. Test files are
// excluded — they are not what a consumer links against.
func TestNothingHereCanReachTheNetwork(t *testing.T) {
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
				t.Errorf("%s imports %q, which is not on this test's allowlist. If it cannot reach the "+
					"network, spawn a process or read the clock, add it; otherwise the fake has stopped "+
					"being deterministic", name, path)
			}
		}
	}

	// Inspecting nothing is the quietest pass there is, and the whole package is
	// two files away from being renamed out from under this test.
	if inspected < 2 {
		t.Fatalf("inspected %d non-test file(s) in this package; expected the fake's own sources", inspected)
	}
}
