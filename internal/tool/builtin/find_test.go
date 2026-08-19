package builtin_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/tool/builtin"
)

type findCase struct {
	name  string
	files map[string]string
	args  map[string]any

	want  []string
	total int // Details.Total; zero means len(want)

	// content pins the model-visible text for the cases whose whole point is its
	// shape, and for every case that matches nothing: an empty listing and a
	// sentence saying so are not distinguishable from the path list alone.
	content string

	isError       bool
	errorContains string
}

var findCases = []findCase{{
	name: "a leading double star matches at every depth, the root included",
	files: map[string]string{
		"main.go":       "",
		"a/b.go":        "",
		"a/b/c/deep.go": "",
		"a/notes.txt":   "",
	},
	args: map[string]any{"pattern": "**/*.go"},
	want: []string{"a/b.go", "a/b/c/deep.go", "main.go"},
}, {
	name: "a star stops at a path separator",
	files: map[string]string{
		"main.go":  "",
		"a/b.go":   "",
		"main.txt": "",
	},
	args: map[string]any{"pattern": "*.go"},
	want: []string{"main.go"},
}, {
	name: "a pattern with no double star anchors at the workspace root",
	files: map[string]string{
		"internal/a.go":   "",
		"internal/b/c.go": "",
		"a.go":            "",
	},
	args: map[string]any{"pattern": "internal/*.go"},
	want: []string{"internal/a.go"},
}, {
	name: "a double star in the middle spans any number of segments",
	files: map[string]string{
		"internal/a_test.go":     "",
		"internal/x/y/b_test.go": "",
		"internal/x/y/b.go":      "",
		"cmd/c_test.go":          "",
	},
	args: map[string]any{"pattern": "internal/**/*_test.go"},
	want: []string{"internal/a_test.go", "internal/x/y/b_test.go"},
}, {
	name: "an alternation matches either branch",
	files: map[string]string{
		"a/x.go": "",
		"b/x.go": "",
		"c/x.go": "",
	},
	args: map[string]any{"pattern": "{a,b}/*.go"},
	want: []string{"a/x.go", "b/x.go"},
}, {
	name: "a character class matches one character of a name",
	files: map[string]string{
		"v1.go": "",
		"v2.go": "",
		"v9.go": "",
	},
	args: map[string]any{"pattern": "v[12].go"},
	want: []string{"v1.go", "v2.go"},
}, {
	name: "a question mark matches exactly one character",
	files: map[string]string{
		"a.go":  "",
		"ab.go": "",
	},
	args: map[string]any{"pattern": "?.go"},
	want: []string{"a.go"},
}, {
	name: "a root gitignore excludes a directory",
	files: map[string]string{
		".gitignore":      "vendor/\n",
		"a.go":            "",
		"vendor/dep/x.go": "",
	},
	args: map[string]any{"pattern": "**/*.go"},
	want: []string{"a.go"},
}, {
	name: "a nested gitignore re-includes with a negation",
	files: map[string]string{
		"sub/.gitignore": "*.log\n!keep.log\n",
		"sub/keep.log":   "",
		"sub/skip.log":   "",
		"root.log":       "",
	},
	args: map[string]any{"pattern": "**/*.log"},
	want: []string{"root.log", "sub/keep.log"},
}, {
	// The parent of every fixture workspace carries a .gitignore naming this file
	// (see newGrepWorkspace). Ignore files above the workspace are outside its
	// reach, so find may not honour one any more than grep may.
	name:  "an ignore file above the workspace is not read",
	files: map[string]string{"hidden-by-parent.txt": ""},
	args:  map[string]any{"pattern": "**/*.txt"},
	want:  []string{"hidden-by-parent.txt"},
}, {
	name: "the git directory is never listed",
	files: map[string]string{
		".git/config":     "",
		".git/hooks/x.sh": "",
		"a.sh":            "",
	},
	args: map[string]any{"pattern": "**"},
	want: []string{"a.sh"},
}, {
	name: "dotfiles and dot-directories are listed",
	files: map[string]string{
		".env":                    "",
		".github/workflows/w.yml": "",
	},
	args: map[string]any{"pattern": "**"},
	want: []string{".env", ".github/workflows/w.yml"},
}, {
	// The pattern that matches everything is the one that shows a directory is
	// not a result: "sub" and "sub/deep" match it as strings and must not appear.
	name: "only files come back, never a directory",
	files: map[string]string{
		"sub/deep/a.txt": "",
		"b.txt":          "",
	},
	args: map[string]any{"pattern": "**"},
	want: []string{"b.txt", "sub/deep/a.txt"},
}, {
	// Sorted by path, "a-b.go" comes before "a/x.go" because - sorts below /. The
	// walk emits them the other way round, so this fails if the paths are reported
	// in the order they were found.
	name: "path order is not walk order",
	files: map[string]string{
		"a/x.go": "",
		"a-b.go": "",
	},
	args: map[string]any{"pattern": "**/*.go"},
	want: []string{"a-b.go", "a/x.go"},
}, {
	name: "path narrows the walk to a subtree",
	files: map[string]string{
		"a.go":     "",
		"sub/b.go": "",
	},
	args: map[string]any{"pattern": "**/*.go", "path": "sub"},
	want: []string{"sub/b.go"},
}, {
	// The frame the description has to be explicit about: path chooses the subtree
	// to walk, and the pattern is still matched against the whole path, so one
	// that would match inside that subtree on its own matches nothing here.
	name:    "path does not re-root the pattern",
	files:   map[string]string{"sub/b.go": ""},
	args:    map[string]any{"pattern": "*.go", "path": "sub"},
	content: `No files match "*.go" in sub.`,
}, {
	name:  "a gitignored file is skipped inside a named subtree too",
	files: map[string]string{"sub/.gitignore": "skip.txt\n", "sub/skip.txt": "", "sub/keep.txt": ""},
	args:  map[string]any{"pattern": "**/*.txt", "path": "sub"},
	want:  []string{"sub/keep.txt"},
}, {
	name:  "files past the cap are counted, not returned",
	files: manyFindFiles(150),
	args:  map[string]any{"pattern": "**"},
	want:  manyFindWant(100),
	total: 151,
}, {
	name:    "no matches is a result, not a failure",
	files:   map[string]string{"a.txt": ""},
	args:    map[string]any{"pattern": "**/*.go"},
	content: `No files match "**/*.go" in the workspace.`,
}, {
	name:    "an empty workspace matches nothing",
	files:   map[string]string{},
	args:    map[string]any{"pattern": "**"},
	content: `No files match "**" in the workspace.`,
}, {
	name:          "an empty pattern is refused",
	files:         map[string]string{"a.go": ""},
	args:          map[string]any{"pattern": ""},
	isError:       true,
	errorContains: "no pattern",
}, {
	name:          "an unclosed character class is refused",
	files:         map[string]string{"a.go": ""},
	args:          map[string]any{"pattern": "a[bc.go"},
	isError:       true,
	errorContains: "malformed",
}, {
	name:          "an unclosed alternation is refused",
	files:         map[string]string{"a.go": ""},
	args:          map[string]any{"pattern": "{a,b"},
	isError:       true,
	errorContains: "malformed",
}, {
	// The case the up-front validation exists for: nothing in an empty workspace
	// ever reaches a match call, so a pattern checked only where it is applied
	// would come back here as "no files match" — an answer about the tree for a
	// question that was never well formed.
	name:          "a malformed pattern is refused even with nothing to match against",
	files:         map[string]string{},
	args:          map[string]any{"pattern": "a[bc.go"},
	isError:       true,
	errorContains: "malformed",
}, {
	name:          "a path outside the workspace is refused",
	files:         map[string]string{"a.go": ""},
	args:          map[string]any{"pattern": "**", "path": "../outside"},
	isError:       true,
	errorContains: "outside the workspace",
}, {
	name:          "a path that is not there is refused",
	files:         map[string]string{"a.go": ""},
	args:          map[string]any{"pattern": "**", "path": "nope"},
	isError:       true,
	errorContains: "nope",
}}

func TestFind(t *testing.T) {
	if len(findCases) == 0 {
		t.Fatal("the case table is empty, so this test proves nothing about find")
	}
	for _, tc := range findCases {
		t.Run(tc.name, func(t *testing.T) {
			runFindCase(t, tc)
		})
	}
}

func runFindCase(t *testing.T, tc findCase) {
	t.Helper()

	ws := newGrepWorkspace(t, tc.files)
	res, err := builtin.NewFind(ws).Run(context.Background(), grepArgs(t, tc.args))
	if err != nil {
		t.Fatalf("find(%v) returned the Go error %v; a search it cannot perform is a failed result, "+
			"not a failed tool (design §3.4)", tc.args, err)
	}

	if res.IsError != tc.isError {
		t.Fatalf("IsError = %t, want %t; content was %q", res.IsError, tc.isError, res.Content)
	}
	if tc.isError {
		if !strings.Contains(res.Content, tc.errorContains) {
			t.Errorf("the refusal does not mention %q:\n%s", tc.errorContains, res.Content)
		}
		return
	}

	details, ok := res.Details.(*builtin.FindDetails)
	if !ok {
		t.Fatalf("Details is %T, want *builtin.FindDetails", res.Details)
	}
	if diff := findPathDiff(details.Paths, tc.want); diff != "" {
		t.Errorf("paths:\n%s", diff)
	}
	total := tc.total
	if total == 0 {
		total = len(tc.want)
	}
	if details.Total != total {
		t.Errorf("Details.Total = %d, want %d", details.Total, total)
	}

	switch {
	case tc.content != "":
		if res.Content != tc.content {
			t.Errorf("Content =\n%q\nwant\n%q", res.Content, tc.content)
		}
	default:
		checkFindListing(t, res.Content, tc.want, total)
	}
}

// checkFindListing holds the model-visible contract: one path per line, in the
// order Details reports them, and a truncation notice exactly when there were
// more files than the listing carries.
func checkFindListing(t *testing.T, content string, want []string, total int) {
	t.Helper()

	if len(want) == 0 {
		t.Fatal("a case with no expected paths must pin its content: an empty listing and the " +
			"sentence that replaces one are the same string here")
	}

	listing, notice, _ := strings.Cut(content, "\n\n")
	lines := strings.Split(strings.TrimRight(listing, "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("Content has %d listed lines, want %d:\n%s", len(lines), len(want), content)
	}
	for i, p := range want {
		if lines[i] != p {
			t.Errorf("Content line %d = %q, want %q", i+1, lines[i], p)
		}
	}

	switch truncated := total > len(want); {
	case truncated && !strings.Contains(notice, fmt.Sprintf("first %d of %d files", len(want), total)):
		t.Errorf("Content does not say how many files were left out:\n%s", content)
	case !truncated && strings.TrimSpace(notice) != "":
		t.Errorf("Content carries a truncation notice for a complete result:\n%s", content)
	}
}

// TestFindDoesNotListSymlinks covers both kinds, because only one of them tests
// this tool. A link out of the workspace is refused by the workspace itself
// whatever find does; a link that stays inside resolves perfectly well, and
// listing it would name a file the model can already reach under its real name.
func TestFindDoesNotListSymlinks(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "ws")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{dir, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("creating %s: %v", d, err)
		}
	}
	writeGrepFile(t, outside, "secret.go", "")
	writeGrepFile(t, dir, "a.go", "")
	writeGrepFile(t, dir, "sub/b.go", "")

	// Relative targets, the way a repository writes them, and the only form that
	// reaches this tool: os.Root refuses a symlink with an absolute target
	// outright, so absolute links here would be testing the workspace instead.
	links := map[string]string{
		"out.go": "../outside/secret.go",
		"outdir": "../outside",
		"dup.go": "a.go",
		"dupdir": "sub",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Skipf("this platform will not create the symlink %s: %v", name, err)
		}
	}

	ws := openGrepWorkspace(t, dir)
	res, err := builtin.NewFind(ws).Run(context.Background(), grepArgs(t, map[string]any{"pattern": "**/*.go"}))
	if err != nil {
		t.Fatalf("find returned the Go error %v", err)
	}
	details, ok := res.Details.(*builtin.FindDetails)
	if !ok {
		t.Fatalf("Details is %T, want *builtin.FindDetails", res.Details)
	}
	if diff := findPathDiff(details.Paths, []string{"a.go", "sub/b.go"}); diff != "" {
		t.Errorf("a symlink was listed or followed:\n%s", diff)
	}
}

// TestFindAndGrepSelectTheSameFiles pins the shared walk. The two tools answer
// different questions over one set of files, and a rule added to either walk
// alone would make find list a file grep will not search, or the reverse.
func TestFindAndGrepSelectTheSameFiles(t *testing.T) {
	files := map[string]string{
		// The leading comment is a line the ignore parser drops, and it is what puts
		// the word in a file whose content is otherwise all patterns.
		".gitignore":              "# target\nvendor/\n*.log\n",
		".git/config":             "target in the object store\n",
		".env":                    "target dotfile\n",
		".github/workflows/w.yml": "target workflow\n",
		"a.go":                    "target source\n",
		"sub/b.go":                "target nested\n",
		"vendor/dep/c.go":         "target vendored\n",
		"x.log":                   "target logged\n",
	}
	ws := newGrepWorkspace(t, files)

	res, err := builtin.NewFind(ws).Run(context.Background(), grepArgs(t, map[string]any{"pattern": "**"}))
	if err != nil {
		t.Fatalf("find returned the Go error %v", err)
	}
	found, ok := res.Details.(*builtin.FindDetails)
	if !ok {
		t.Fatalf("find Details is %T, want *builtin.FindDetails", res.Details)
	}
	if len(found.Paths) == 0 {
		t.Fatal("find listed nothing, so this compares two empty sets")
	}

	// Every fixture file's content carries the word, so grep reaches each of them
	// that its walk selects and no further filter narrows the comparison.
	res, err = builtin.NewGrep(ws, "").Run(context.Background(), grepArgs(t, map[string]any{"pattern": "target"}))
	if err != nil {
		t.Fatalf("grep returned the Go error %v", err)
	}
	searched, ok := res.Details.(*builtin.GrepDetails)
	if !ok {
		t.Fatalf("grep Details is %T, want *builtin.GrepDetails", res.Details)
	}

	var paths []string
	for _, m := range searched.Matches {
		if len(paths) == 0 || paths[len(paths)-1] != m.Path {
			paths = append(paths, m.Path)
		}
	}
	if diff := findPathDiff(found.Paths, paths); diff != "" {
		t.Errorf("find lists a different set of files than grep searches:\n%s", diff)
	}
}

func TestFindReportsAnInterruptedTurnAsAGoError(t *testing.T) {
	ws := newGrepWorkspace(t, map[string]string{"a.go": ""})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := builtin.NewFind(ws).Run(ctx, grepArgs(t, map[string]any{"pattern": "**"}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled: a torn-down turn is the tool failing to "+
			"run, not an observation the model can act on (design §12). Result was %+v", err, res)
	}
}

func TestFindNeedsAWorkspace(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewFind(nil) returned a tool that has no route to the filesystem")
		}
	}()
	builtin.NewFind(nil)
}

// findPathDiff describes the first difference between two path lists, or "".
func findPathDiff(got, want []string) string {
	for i := range max(len(got), len(want)) {
		switch {
		case i >= len(got):
			return fmt.Sprintf("missing path %d: %q", i, want[i])
		case i >= len(want):
			return fmt.Sprintf("unexpected path %d: %q", i, got[i])
		case got[i] != want[i]:
			return fmt.Sprintf("path %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
	return ""
}

// manyFindFiles is n files in a directory plus one beside it, which is a tree
// whose walk order is not its path order: the walk descends into "a" before it
// reaches "a.txt", because a directory entry sorts before the longer name, while
// as a path "a.txt" sorts below "a/000.txt" because "." is under "/". So a cap
// applied as the paths arrive drops "a.txt", and one applied in path order keeps
// it first.
func manyFindFiles(n int) map[string]string {
	files := map[string]string{"a.txt": ""}
	for i := range n {
		files[fmt.Sprintf("a/%03d.txt", i)] = ""
	}
	return files
}

func manyFindWant(n int) []string {
	want := make([]string, 0, n)
	want = append(want, "a.txt")
	for i := range n - 1 {
		want = append(want, fmt.Sprintf("a/%03d.txt", i))
	}
	return want
}
