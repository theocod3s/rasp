// Package rasp_test holds repository-level tests: the ones that check the shape
// of the tree rather than the behaviour of anything in it.
//
// Design §2 is mostly a table of exclusions — what each package must *not*
// contain — and a rule that lives only in a design document is one nobody reads
// at the moment they are about to break it. These tests move that rule to where
// it fails the build. The workspace boundary is slated for the same treatment
// ("enforced by a lint or test") when it lands.
//
// The package list is parsed out of docs/design.md rather than copied into this
// file. A copy would let the tree and the document drift apart one green build
// at a time, which is the failure these tests exist to prevent — they should not
// reproduce it.
package rasp_test

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build"
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

const (
	designDoc = "docs/design.md"

	// packageLayoutHeading is the section of design.md holding the tree.
	packageLayoutHeading = "## 2. Package layout"

	// exclusionMarker introduces what does not belong in a package. Requiring
	// one fixed phrase keeps the convention greppable and the check
	// unambiguous.
	exclusionMarker = "Does not contain:"

	// minExclusionLen is how much text must follow the marker. It exists to
	// reject a marker satisfied by a shrug ("Does not contain: nothing much")
	// while staying well under the shortest real exclusion in the tree.
	minExclusionLen = 30

	// topLevelGoDirs are the only top-level directories design §2 gives Go
	// code to.
	topLevelCmd      = "cmd"
	topLevelInternal = "internal"
)

// TestInternalTreeMatchesDesign fails when a package appears in internal/ that
// design §2 does not list, or §2 lists one the tree does not have.
func TestInternalTreeMatchesDesign(t *testing.T) {
	designed := designPackages(t)
	found := internalPackages(t)

	for _, pkg := range designed {
		if !slices.Contains(found, pkg) {
			t.Errorf("%s lists %s, but it is not in the tree",
				designDoc, path.Join(topLevelInternal, pkg))
		}
	}
	for _, pkg := range found {
		if !slices.Contains(designed, pkg) {
			t.Errorf("%s exists but %s §2 does not list it — add it to the tree "+
				"and give it a row in the table saying what it must not contain",
				path.Join(topLevelInternal, pkg), designDoc)
		}
	}
}

// TestEveryInternalPackageDocumentsWhatItExcludes enforces the "Does not
// contain:" paragraph required of every package doc comment, kept as a test so
// it also holds for the next package added.
func TestEveryInternalPackageDocumentsWhatItExcludes(t *testing.T) {
	for _, pkg := range internalPackages(t) {
		t.Run(pkg, func(t *testing.T) {
			dir := filepath.Join(topLevelInternal, filepath.FromSlash(pkg))
			name, doc := packageDoc(t, dir)

			if doc == "" {
				t.Fatalf("no package doc comment; every internal package states its single "+
					"responsibility and what does not belong in it (%s §2). Add a doc.go "+
					"starting %q", designDoc, "// Package "+name+" ...")
			}
			if !strings.HasPrefix(doc, "Package "+name+" ") {
				t.Errorf("doc comment should open %q, per Go convention; got %q",
					"Package "+name+" ...", firstLine(doc))
			}

			// The exclusion has to be its own paragraph rather than a phrase
			// buried in one, so it cannot be satisfied in passing.
			para := exclusionParagraph(doc)
			switch {
			case para == "":
				t.Errorf("doc comment has no paragraph opening %q, so it never says what the "+
					"package excludes. That half is the load-bearing one — %s §2 is mostly a "+
					"list of exclusions, and an exclusion nobody can find is not a constraint",
					exclusionMarker, designDoc)
			case len(strings.TrimSpace(strings.TrimPrefix(para, exclusionMarker))) < minExclusionLen:
				t.Errorf("the %q paragraph is too thin to be telling anyone anything: %q",
					exclusionMarker, para)
			}
		})
	}
}

// TestNoUndesignedTopLevelPackages closes the escape route from the test above:
// design §2 gives Go code to cmd/ and internal/ only, so Go appearing anywhere
// else is as much a design change as a new internal package.
//
// The one exception is a repo-level _test.go at the module root, which has no
// package of its own to live in — this file is one.
func TestNoUndesignedTopLevelPackages(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading module root: %v", err)
	}

	var rootGo []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() {
			if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
				rootGo = append(rootGo, name)
			}
			continue
		}
		if ignoredDir(name) || name == topLevelCmd || name == topLevelInternal {
			continue
		}
		if pkgs := goPackagesUnder(t, name); len(pkgs) > 0 {
			t.Errorf("%s/ holds Go packages %v, but %s §2 puts Go under %s/ and %s/ only",
				name, pkgs, designDoc, topLevelCmd, topLevelInternal)
		}
	}

	if len(rootGo) > 0 {
		t.Errorf("the module root holds non-test Go %v, but %s §2 puts Go under %s/ and %s/ "+
			"only; repo-level _test.go files are the one exception", rootGo, designDoc,
			topLevelCmd, topLevelInternal)
	}
}

// designPackages reads the internal/ tree out of design §2 and returns its
// packages as slash-separated paths relative to internal/. Parsing the document
// is what makes the test's subject design §2 itself rather than a copy of it.
func designPackages(t *testing.T) []string {
	t.Helper()

	src, err := os.ReadFile(designDoc)
	if err != nil {
		t.Fatalf("reading %s: %v", designDoc, err)
	}

	var (
		pkgs       []string
		stack      []string
		inInternal bool
	)
	for _, line := range packageLayoutBlock(t, string(src)) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		entry := strings.Fields(line)[0]

		// Top-level entries reset the walk: the block covers cmd/ as well, and
		// only the internal/ subtree is ours.
		if indent == 0 {
			inInternal = entry == topLevelInternal+"/"
			stack = stack[:0]
			continue
		}
		// Files (main.go, run.go) are listed alongside directories; only
		// directories are packages.
		if !inInternal || !strings.HasSuffix(entry, "/") {
			continue
		}

		depth := indent / 2
		if depth < 1 || depth-1 > len(stack) {
			t.Fatalf("%s §2: cannot read the tree at %q — expected two spaces per level",
				designDoc, strings.TrimRight(line, " "))
		}
		stack = append(stack[:depth-1], strings.TrimSuffix(entry, "/"))
		pkgs = append(pkgs, strings.Join(stack, "/"))
	}

	if len(pkgs) == 0 {
		t.Fatalf("%s: found no packages under %s/ in %q — has the section been reformatted?",
			designDoc, topLevelInternal, packageLayoutHeading)
	}
	slices.Sort(pkgs)
	return pkgs
}

// packageLayoutBlock returns the lines of the first fenced code block under the
// package-layout heading.
func packageLayoutBlock(t *testing.T, doc string) []string {
	t.Helper()

	lines := strings.Split(doc, "\n")
	start := slices.Index(lines, packageLayoutHeading)
	if start < 0 {
		t.Fatalf("%s: no %q heading", designDoc, packageLayoutHeading)
	}

	open := -1
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "```") {
			open = i
			break
		}
	}
	if open < 0 {
		t.Fatalf("%s: no code block under %q", designDoc, packageLayoutHeading)
	}
	for i := open + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "```") {
			return lines[open+1 : i]
		}
	}
	t.Fatalf("%s: unterminated code block under %q", designDoc, packageLayoutHeading)
	return nil
}

// internalPackages returns every Go package under internal/, as slash-separated
// paths relative to internal/.
func internalPackages(t *testing.T) []string {
	t.Helper()

	pkgs := goPackagesUnder(t, topLevelInternal)
	if len(pkgs) == 0 {
		t.Fatalf("found no packages under %s/ — is the test running from the module root?",
			topLevelInternal)
	}
	return pkgs
}

// goPackagesUnder returns the Go packages in and below root, relative to root.
// "Go package" means what the toolchain means by it, so testdata/ and the
// _- and .-prefixed directories it ignores are ignored here too — otherwise the
// golden files, cassettes and fuzz corpora design §13 calls for would each be
// reported as an undesigned package.
func goPackagesUnder(t *testing.T, root string) []string {
	t.Helper()

	var pkgs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if p != root && ignoredDir(d.Name()) {
			return fs.SkipDir
		}
		isPkg, err := isGoPackage(p)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if !isPkg {
			return nil // a directory on the way to one, not a package
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		pkgs = append(pkgs, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s/: %v", root, err)
	}

	slices.Sort(pkgs)
	return pkgs
}

// ignoredDir reports whether the Go toolchain ignores a directory of this name.
func ignoredDir(name string) bool {
	return name == "testdata" || strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".")
}

// releaseTargets is design §14's build matrix.
var releaseTargets = []struct{ goos, goarch string }{
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"linux", "amd64"}, {"linux", "arm64"},
	{"windows", "amd64"}, {"windows", "arm64"},
}

// isGoPackage reports whether dir is a Go package for any release target.
//
// Asking only about the host would make these tests platform-dependent, and
// design §2 names a per-platform package (wakelock) that CI will run on both
// linux and macOS. A package the host cannot build would otherwise report as
// missing from the tree — and, worse, generate no subtest at all, so its doc
// comment would go unchecked on the very platform where nobody is looking.
func isGoPackage(dir string) (bool, error) {
	for _, target := range releaseTargets {
		ctx := build.Default
		ctx.GOOS, ctx.GOARCH = target.goos, target.goarch
		ctx.CgoEnabled = false // §14 fixes CGO_ENABLED=0, so cgo-only files build nowhere

		if _, err := ctx.ImportDir(dir, 0); err != nil {
			var noGo *build.NoGoError
			if errors.As(err, &noGo) {
				continue // excluded here; another target may still want it
			}
			return false, err
		}
		return true, nil
	}
	return false, nil
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
	return name, strings.Join(docs, "\n\n")
}

// exclusionParagraph returns the paragraph introducing what the package
// excludes, or "" if no paragraph opens with the marker.
func exclusionParagraph(doc string) string {
	for para := range strings.SplitSeq(doc, "\n\n") {
		if strings.HasPrefix(para, exclusionMarker) {
			return strings.TrimSpace(para)
		}
	}
	return ""
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
