package rasp_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	// workspacePkg holds the os.Root handle every file tool reaches the
	// filesystem through (design §2).
	workspacePkg = "github.com/theocod3s/rasp/internal/workspace"

	// toolPkg and builtinPkg are named rather than derived so a §2 rename cannot
	// quietly empty this test's subject: builtin is where the eight tools land,
	// and a run that policed neither would still be green.
	toolPkg    = "tool"
	builtinPkg = "tool/builtin"
)

// osAllowed is every identifier in os that a package under internal/tool may
// name. An allowlist rather than a list of the calls to avoid, because a denylist
// names only the ones somebody already thought of and os keeps growing.
var osAllowed = []string{
	// Flags and modes handed to workspace.OpenFile, WriteFile and MkdirAll.
	"O_APPEND", "O_CREATE", "O_EXCL", "O_RDONLY", "O_RDWR", "O_SYNC", "O_TRUNC", "O_WRONLY",
	"FileMode", "ModeDir", "ModePerm", "ModeSymlink",

	// Error values a caller compares against with errors.Is.
	"ErrClosed", "ErrExist", "ErrInvalid", "ErrNotExist", "ErrPermission",

	// Types that cross the workspace boundary rather than reach past it.
	"DirEntry", "File", "FileInfo", "LinkError", "PathError",

	// The bash tool builds a child's environment, signals it, and says when a
	// cancel arrived too late to signal anything. None of it is a path os.Root
	// could confine, and bash is gated by permission instead (design §7.3a).
	"Environ", "Getenv", "LookupEnv",
	"ErrProcessDone", "Interrupt", "Kill", "Process", "ProcessState", "Signal",

	// CreateTemp is bash spilling output too long to return, and it is the one
	// entry here that does open a path. It belongs outside the workspace: the
	// spill is rasp's own file, in the OS temp directory, under a name rasp
	// generates and the model never supplies — so the question os.Root answers,
	// "did this path escape the root", is not one that arises. Writing it inside
	// the workspace would be the bug, dropping build logs into the user's repo.
	// Nothing else in os grows this exemption: os.Remove, os.Rename and the rest
	// take a caller's path and stay refused.
	"CreateTemp",
}

// filepathForbidden is the part of path/filepath that consults the filesystem, or
// the process's own working directory — which is not necessarily the workspace
// root. The rest of the package is string arithmetic and stays allowed: a tool
// has to be able to take the Base of a name.
var filepathForbidden = []string{"Abs", "EvalSymlinks", "Glob", "Walk", "WalkDir"}

// forbiddenImports are packages that offer a tool nothing workspace does not
// already serve. syscall is deliberately absent: the bash tool needs SysProcAttr
// to kill a process group (internals §6.2), and nobody reaches for syscall to
// read a file by accident — which is the mistake this test exists to catch.
var forbiddenImports = []string{"io/ioutil"}

// TestFileToolsReachTheFilesystemOnlyThroughWorkspace enforces the one design §2
// rule stated as a route rather than an exclusion: every file tool goes through
// internal/workspace, never os directly (prd §6.6). A tool calling os.ReadFile
// gets neither the ../ refusal nor the symlink one, and gets them back only if
// somebody notices.
//
// The tools themselves land in later milestones, so the subject is the packages
// §2 reserves for them, and the check holds from the moment the first one arrives.
func TestFileToolsReachTheFilesystemOnlyThroughWorkspace(t *testing.T) {
	// The route has to exist for the rule to mean anything.
	if files := nonTestGoFiles(t, filepath.Join(topLevelInternal, "workspace")); len(files) == 0 {
		t.Fatalf("internal/workspace holds no non-test Go files, so this test is pointing every "+
			"tool at a package that does not exist (%s §2)", designDoc)
	}

	for _, pkg := range toolPackages(t) {
		t.Run(pkg, func(t *testing.T) {
			dir := filepath.Join(topLevelInternal, filepath.FromSlash(pkg))
			files := nonTestGoFiles(t, dir)
			if len(files) == 0 {
				t.Fatalf("%s: nothing to inspect. Every package under %s/ carries at least a "+
					"doc.go, so an empty result means this test is looking in the wrong place — "+
					"and inspecting nothing is the quietest pass there is", dir, topLevelInternal)
			}
			for _, file := range files {
				checkFilesystemAccess(t, file)
			}
		})
	}
}

// toolPackages is internal/tool and everything design §2 puts under it.
func toolPackages(t *testing.T) []string {
	t.Helper()

	var pkgs []string
	for _, pkg := range designPackages(t) {
		if pkg == toolPkg || strings.HasPrefix(pkg, toolPkg+"/") {
			pkgs = append(pkgs, pkg)
		}
	}

	for _, required := range []string{toolPkg, builtinPkg} {
		if !slices.Contains(pkgs, required) {
			t.Fatalf("%s §2 no longer lists %s, so this test would police %v and miss the tools "+
				"entirely. Point it at wherever they moved to",
				designDoc, path.Join(topLevelInternal, required), pkgs)
		}
	}
	return pkgs
}

// checkFilesystemAccess reports every reference in one file that reaches the
// filesystem without going through workspace.
func checkFilesystemAccess(t *testing.T, file string) {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}

	// Keyed by the local name, so an alias cannot slip past a check that only
	// looked for the word "os".
	imported := map[string]string{}
	for _, spec := range f.Imports {
		pkg, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("%s: cannot read the import path %s: %v", file, spec.Path.Value, err)
		}
		if slices.Contains(forbiddenImports, pkg) {
			t.Errorf("%s imports %q; file access here goes through %s", file, pkg, workspacePkg)
			continue
		}

		local := path.Base(pkg)
		if spec.Name != nil {
			local = spec.Name.Name
		}
		switch local {
		case "_":
			continue
		case ".":
			if pkg == "os" || pkg == "path/filepath" {
				t.Errorf("%s dot-imports %q, which brings every one of its functions into scope "+
					"unqualified and puts them beyond this check. Import it by name", file, pkg)
			}
			continue
		}
		imported[local] = pkg
	}

	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}

		at := fset.Position(sel.Pos())
		switch imported[ident.Name] {
		case "os":
			if !slices.Contains(osAllowed, sel.Sel.Name) {
				t.Errorf("%s:%d uses os.%s. A file tool reaches the filesystem through %s, which is "+
					"what refuses ../ escapes and symlinks leaving the workspace (design §2, "+
					"prd §6.6). If os.%s cannot open, create, list, stat, move or remove a path — a "+
					"flag, a mode, an error value, a type — add it to osAllowed with the group it "+
					"belongs to", file, at.Line, sel.Sel.Name, workspacePkg, sel.Sel.Name)
			}
		case "path/filepath":
			if slices.Contains(filepathForbidden, sel.Sel.Name) {
				t.Errorf("%s:%d uses filepath.%s, which resolves against the filesystem or the "+
					"process's working directory rather than the workspace root. %s answers the "+
					"same questions inside the confinement", file, at.Line, sel.Sel.Name, workspacePkg)
			}
		}
		return true
	})
}

// nonTestGoFiles lists the Go files in dir that a consumer links against, sorted.
// A directory it cannot read is fatal rather than empty: this test's whole value
// is in what it finds, so a missing subject must not read as a clean one.
func nonTestGoFiles(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	slices.Sort(files)
	return files
}
