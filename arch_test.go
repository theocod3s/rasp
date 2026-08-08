// Package rasp_test holds repository-level tests: the ones that check the shape
// of the tree rather than the behaviour of anything in it.
//
// Design §2 is mostly a table of exclusions — what each package must *not*
// contain — and a rule that lives only in a design document is one nobody reads
// at the moment they are about to break it. These tests move that rule to where
// it fails the build. M1-09 asks for the same treatment ("enforced by a lint or
// test") for the workspace boundary.
package rasp_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// designPackages is the internal/ tree exactly as design §2 specifies it, as
// paths relative to internal/. Adding a package here is a design decision:
// update §2 and this list together, or the tree and the document drift and the
// document loses.
var designPackages = []string{
	"agent",
	"auth",
	"compact",
	"config",
	"headless",
	"llm",
	"llm/anthropic",
	"llm/fake",
	"llm/openaicompat",
	"llm/retry",
	"logx",
	"mcp",
	"permission",
	"prompt",
	"session",
	"tool",
	"tool/builtin",
	"tool/edit",
	"tui",
	"tui/chat",
	"tui/dialog",
	"tui/diffview",
	"tui/styles",
	"wakelock",
	"workspace",
}

// exclusionMarker is the phrase every package doc uses to introduce what does
// not belong in the package. Requiring one fixed phrase makes the convention
// greppable and the check unambiguous.
const exclusionMarker = "Does not contain"

// TestInternalTreeMatchesDesign fails when a package appears in internal/
// without a corresponding row in design §2, or disappears from the tree while
// §2 still lists it.
func TestInternalTreeMatchesDesign(t *testing.T) {
	found := internalPackages(t)

	for _, pkg := range designPackages {
		if !slices.Contains(found, pkg) {
			t.Errorf("design §2 lists internal/%s, but it is not in the tree", pkg)
		}
	}
	for _, pkg := range found {
		if !slices.Contains(designPackages, pkg) {
			t.Errorf("internal/%s is in the tree but not in design §2 — add a row to the table "+
				"(including what the package must not contain) and to designPackages here", pkg)
		}
	}
}

// TestEveryInternalPackageDocumentsWhatItExcludes is M0-01's second acceptance
// criterion, kept as a test so it also holds for the next package added.
func TestEveryInternalPackageDocumentsWhatItExcludes(t *testing.T) {
	for _, pkg := range internalPackages(t) {
		t.Run(pkg, func(t *testing.T) {
			name, doc := packageDoc(t, filepath.Join("internal", filepath.FromSlash(pkg)))

			switch {
			case doc == "":
				t.Fatalf("no package doc comment; every internal package states its single "+
					"responsibility and what does not belong in it (design §2). Add a doc.go "+
					"starting %q", "// Package "+name+" ...")
			case !strings.HasPrefix(doc, "Package "+name+" "):
				t.Errorf("doc comment should open %q, per Go convention; got %q",
					"Package "+name+" ...", firstLine(doc))
			}

			if !strings.Contains(doc, exclusionMarker) {
				t.Errorf("doc comment never says what the package excludes: no %q paragraph. "+
					"That half is the load-bearing one — design §2 is mostly a list of "+
					"exclusions, and an exclusion nobody can find is not a constraint",
					exclusionMarker)
			}
		})
	}
}

// internalPackages returns every directory under internal/ holding Go source,
// as slash-separated paths relative to internal/.
func internalPackages(t *testing.T) []string {
	t.Helper()

	var pkgs []string
	err := filepath.WalkDir("internal", func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
				rel, err := filepath.Rel("internal", p)
				if err != nil {
					return err
				}
				pkgs = append(pkgs, path.Clean(filepath.ToSlash(rel)))
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("found no packages under internal/ — is the test running from the module root?")
	}

	slices.Sort(pkgs)
	return pkgs
}

// packageDoc parses dir and returns the package name along with its package doc
// comment. Test files are ignored: a doc comment on one of those documents the
// test, not the package.
func packageDoc(t *testing.T, dir string) (name, doc string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	fset := token.NewFileSet()
	var docs []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", filepath.Join(dir, e.Name()), err)
		}
		name = f.Name.Name
		if text := docText(f); text != "" {
			docs = append(docs, text)
		}
	}

	if len(docs) > 1 {
		t.Errorf("%d files carry a package doc comment; Go expects one (conventionally doc.go)", len(docs))
	}
	return name, strings.Join(docs, "\n")
}

func docText(f *ast.File) string {
	if f.Doc == nil {
		return ""
	}
	return strings.TrimSpace(f.Doc.Text())
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
