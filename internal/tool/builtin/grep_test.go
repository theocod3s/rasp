package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tool/builtin"
	"github.com/theocod3s/rasp/internal/workspace"
)

// grepEngine is one of the two search engines, named so a failure says which one
// produced it. skip is set only when the engine cannot run on this host.
type grepEngine struct {
	name string
	path string
	skip string
}

// grepEngines is both engines, always both entries, so the ripgrep half shows up
// in `go test -v` as a skip with its reason rather than not showing up at all.
func grepEngines() []grepEngine {
	engines := []grepEngine{{name: builtin.GrepEngineGo}}
	rg := builtin.RipgrepPath()
	if rg == "" {
		return append(engines, grepEngine{
			name: builtin.GrepEngineRipgrep,
			skip: `no ripgrep on PATH (exec.LookPath("rg") found none), so only the pure-Go engine ran`,
		})
	}
	return append(engines, grepEngine{name: builtin.GrepEngineRipgrep, path: rg})
}

type grepCase struct {
	name  string
	files map[string]string
	args  map[string]any

	want  []builtin.GrepMatch
	total int // Details.Total; zero means len(want)

	// content pins the model-visible text for the cases whose whole point is its
	// shape. The match listing is checked against want for every case regardless.
	content string

	isError       bool
	errorContains string
}

// grepCases is the one table both engines run. Every expectation in it is a
// statement about grep, not about ripgrep or about the fallback: a case that
// passes for one engine and fails for the other is the bug this table exists to
// find.
var grepCases = []grepCase{{
	name: "matches come back sorted by path then line",
	files: map[string]string{
		"b.txt":        "target one\nnothing\ntarget two\n",
		"a/deep/c.txt": "target deep\n",
		"a/b.txt":      "target nested\n",
		"none.txt":     "nothing here\n",
	},
	args: map[string]any{"pattern": "target"},
	want: []builtin.GrepMatch{
		{Path: "a/b.txt", Line: 1, Text: "target nested"},
		{Path: "a/deep/c.txt", Line: 1, Text: "target deep"},
		{Path: "b.txt", Line: 1, Text: "target one"},
		{Path: "b.txt", Line: 3, Text: "target two"},
	},
}, {
	name: "a root gitignore excludes a directory",
	files: map[string]string{
		".gitignore":       "vendor/\n",
		"a.txt":            "target kept\n",
		"vendor/dep/x.txt": "target vendored\n",
	},
	args: map[string]any{"pattern": "target"},
	want: []builtin.GrepMatch{{Path: "a.txt", Line: 1, Text: "target kept"}},
}, {
	name: "a nested gitignore re-includes with a negation",
	files: map[string]string{
		"sub/.gitignore": "*.log\n!keep.log\n",
		"sub/keep.log":   "target keep\n",
		"sub/skip.log":   "target skip\n",
		"root.log":       "target root\n",
	},
	args: map[string]any{"pattern": "target"},
	want: []builtin.GrepMatch{
		{Path: "root.log", Line: 1, Text: "target root"},
		{Path: "sub/keep.log", Line: 1, Text: "target keep"},
	},
}, {
	name: "a leading slash anchors the pattern to its own directory",
	files: map[string]string{
		".gitignore": "/a.txt\n",
		"a.txt":      "target root\n",
		"sub/a.txt":  "target sub\n",
	},
	args: map[string]any{"pattern": "target"},
	want: []builtin.GrepMatch{{Path: "sub/a.txt", Line: 1, Text: "target sub"}},
}, {
	name: "double star spans any number of segments",
	files: map[string]string{
		".gitignore":       "**/gen/\nsrc/**/tmp.txt\n",
		"a/b/gen/x.txt":    "target gen\n",
		"src/x/y/tmp.txt":  "target tmp\n",
		"src/x/y/keep.txt": "target keep\n",
	},
	args: map[string]any{"pattern": "target"},
	want: []builtin.GrepMatch{{Path: "src/x/y/keep.txt", Line: 1, Text: "target keep"}},
}, {
	name: "a trailing slash matches directories only",
	files: map[string]string{
		".gitignore": "build/\n",
		"build":      "target file called build\n",
		"build2/x":   "target inside build2\n",
	},
	args: map[string]any{"pattern": "target"},
	want: []builtin.GrepMatch{
		{Path: "build", Line: 1, Text: "target file called build"},
		{Path: "build2/x", Line: 1, Text: "target inside build2"},
	},
}, {
	name: "a null byte in the first line skips the file",
	files: map[string]string{
		"bin.dat": "\x00\ntarget after the null\n",
		"ok.txt":  "target text\n",
	},
	args: map[string]any{"pattern": "target"},
	want: []builtin.GrepMatch{{Path: "ok.txt", Line: 1, Text: "target text"}},
}, {
	// The one that separates a sniff of the first few kilobytes from the rule
	// ripgrep actually applies: it reads the whole file and, on a null byte, drops
	// every match it had already found.
	name: "a null byte after the matches retracts them",
	files: map[string]string{
		"late.dat": "target one\ntarget two\ntail\x00\n",
		"ok.txt":   "target text\n",
	},
	args: map[string]any{"pattern": "target"},
	want: []builtin.GrepMatch{{Path: "ok.txt", Line: 1, Text: "target text"}},
}, {
	// A binary file reached by directory walk is one ripgrep never mentions; one
	// named on the command line it searches and flags, so the null-byte rule has a
	// second half that only this case reaches.
	name:    "a binary file named directly is still skipped",
	files:   map[string]string{"late.dat": "target one\ntarget two\ntail\x00\n"},
	args:    map[string]any{"pattern": "target", "path": "late.dat"},
	content: `No matches for "target" in late.dat.`,
}, {
	// UTF-16 is the encoding ripgrep transcodes on its own, which would make it
	// search a file every null-byte rule calls binary.
	name:    "a utf-16 file is binary to both engines",
	files:   map[string]string{"wide.txt": "\xff\xfet\x00a\x00r\x00g\x00e\x00t\x00\n\x00"},
	args:    map[string]any{"pattern": "target"},
	content: `No matches for "target" in the workspace.`,
}, {
	// The parent of every fixture workspace carries a .gitignore naming this file
	// (see newGrepWorkspace). Git would not read it from here and neither engine
	// may: ignore files above the workspace are outside its reach.
	name:  "an ignore file above the workspace is not read",
	files: map[string]string{"hidden-by-parent.txt": "target still found\n"},
	args:  map[string]any{"pattern": "target"},
	want:  []builtin.GrepMatch{{Path: "hidden-by-parent.txt", Line: 1, Text: "target still found"}},
}, {
	// .ignore and .rgignore are ripgrep's own inventions. The fallback has no
	// notion of them, so neither engine may honour them.
	name: "a .ignore file is not a .gitignore",
	files: map[string]string{
		".ignore": "a.txt\n",
		"a.txt":   "target found anyway\n",
	},
	args: map[string]any{"pattern": "target"},
	want: []builtin.GrepMatch{{Path: "a.txt", Line: 1, Text: "target found anyway"}},
}, {
	// git's per-clone exclude list lives outside the tree a clone carries, so a
	// search that honoured it would depend on the checkout rather than the repo.
	name: "the repository's private exclude file is not read",
	files: map[string]string{
		".git/info/exclude": "a.txt\n",
		"a.txt":             "target found anyway\n",
	},
	args: map[string]any{"pattern": "target"},
	want: []builtin.GrepMatch{{Path: "a.txt", Line: 1, Text: "target found anyway"}},
}, {
	// Sorted by path, "a-b.txt" comes before "a/x.txt" because - sorts below /.
	// A directory walk emits them the other way round, so this is the case that
	// fails if either engine reports matches in the order it found them.
	name: "path order is not walk order",
	files: map[string]string{
		"a/x.txt": "target in the directory\n",
		"a-b.txt": "target beside it\n",
	},
	args: map[string]any{"pattern": "target"},
	want: []builtin.GrepMatch{
		{Path: "a-b.txt", Line: 1, Text: "target beside it"},
		{Path: "a/x.txt", Line: 1, Text: "target in the directory"},
	},
}, {
	name: "dotfiles and dot-directories are searched",
	files: map[string]string{
		".env":                    "target dotfile\n",
		".github/workflows/w.yml": "target workflow\n",
	},
	args: map[string]any{"pattern": "target"},
	want: []builtin.GrepMatch{
		{Path: ".env", Line: 1, Text: "target dotfile"},
		{Path: ".github/workflows/w.yml", Line: 1, Text: "target workflow"},
	},
}, {
	name: "the git directory is never searched",
	files: map[string]string{
		".git/config":     "target in the object store\n",
		".git/hooks/x.sh": "target hook\n",
		"a.txt":           "target source\n",
	},
	args: map[string]any{"pattern": "target"},
	want: []builtin.GrepMatch{{Path: "a.txt", Line: 1, Text: "target source"}},
}, {
	name: "path narrows the search to a subtree",
	files: map[string]string{
		"a.txt":     "target root\n",
		"sub/b.txt": "target sub\n",
	},
	args: map[string]any{"pattern": "target", "path": "sub"},
	want: []builtin.GrepMatch{{Path: "sub/b.txt", Line: 1, Text: "target sub"}},
}, {
	name: "path may name a single file",
	files: map[string]string{
		"a.txt": "target one\ntarget two\n",
		"b.txt": "target other\n",
	},
	args: map[string]any{"pattern": "target", "path": "a.txt"},
	want: []builtin.GrepMatch{
		{Path: "a.txt", Line: 1, Text: "target one"},
		{Path: "a.txt", Line: 2, Text: "target two"},
	},
}, {
	name: "a file named directly is searched even when a gitignore covers it",
	files: map[string]string{
		".gitignore": "*.log\n",
		"x.log":      "target ignored\n",
	},
	args: map[string]any{"pattern": "target", "path": "x.log"},
	want: []builtin.GrepMatch{{Path: "x.log", Line: 1, Text: "target ignored"}},
}, {
	name:  "the search is case sensitive",
	files: map[string]string{"a.txt": "Target upper\ntarget lower\n"},
	args:  map[string]any{"pattern": "target"},
	want:  []builtin.GrepMatch{{Path: "a.txt", Line: 2, Text: "target lower"}},
}, {
	name:  "an inline flag turns case sensitivity off",
	files: map[string]string{"a.txt": "Target upper\ntarget lower\n"},
	args:  map[string]any{"pattern": "(?i)target"},
	want: []builtin.GrepMatch{
		{Path: "a.txt", Line: 1, Text: "Target upper"},
		{Path: "a.txt", Line: 2, Text: "target lower"},
	},
}, {
	name:  "carriage returns do not reach the model",
	files: map[string]string{"a.txt": "target crlf\r\nsecond\r\n"},
	args:  map[string]any{"pattern": "target"},
	want:  []builtin.GrepMatch{{Path: "a.txt", Line: 1, Text: "target crlf"}},
}, {
	name:  "a regexp anchor works",
	files: map[string]string{"a.txt": "target at the start\nnot target here\n"},
	args:  map[string]any{"pattern": "^target"},
	want:  []builtin.GrepMatch{{Path: "a.txt", Line: 1, Text: "target at the start"}},
}, {
	name:    "no matches is a result, not a failure",
	files:   map[string]string{"a.txt": "nothing here\n"},
	args:    map[string]any{"pattern": "target"},
	content: `No matches for "target" in the workspace.`,
}, {
	name:    "no matches names the subtree that was searched",
	files:   map[string]string{"sub/a.txt": "nothing here\n"},
	args:    map[string]any{"pattern": "target", "path": "sub"},
	content: `No matches for "target" in sub.`,
}, {
	name:  "a long line is clipped",
	files: map[string]string{"a.txt": "target " + strings.Repeat("x", 2000) + "\n"},
	args:  map[string]any{"pattern": "target"},
	want:  []builtin.GrepMatch{{Path: "a.txt", Line: 1, Text: ("target " + strings.Repeat("x", 2000))[:512] + "…"}},
}, {
	name:  "matches past the cap are counted, not returned",
	files: manyLines(150),
	args:  map[string]any{"pattern": "target"},
	want:  manyLinesWant(100),
	total: 150,
}, {
	// The cross-file case, and the one that catches a cap applied in arrival
	// order: the pure-Go walk is alphabetical and ripgrep's threads are not, so
	// the hundred that survive have to be chosen by sort order rather than by
	// whichever file finished first.
	name:  "the cap keeps the lowest matches by path, whatever order they arrive in",
	files: manyFiles(150),
	args:  map[string]any{"pattern": "target"},
	want:  manyFilesWant(100),
	total: 150,
}, {
	name:          "an invalid regexp is refused",
	files:         map[string]string{"a.txt": "target\n"},
	args:          map[string]any{"pattern": "a(b"},
	isError:       true,
	errorContains: "missing closing )",
}, {
	name:          "an empty pattern is refused",
	files:         map[string]string{"a.txt": "target\n"},
	args:          map[string]any{"pattern": ""},
	isError:       true,
	errorContains: "no pattern",
}, {
	name:          "a path outside the workspace is refused",
	files:         map[string]string{"a.txt": "target\n"},
	args:          map[string]any{"pattern": "target", "path": "../outside"},
	isError:       true,
	errorContains: "outside the workspace",
}, {
	name:          "a path that is not there is refused",
	files:         map[string]string{"a.txt": "target\n"},
	args:          map[string]any{"pattern": "target", "path": "nope"},
	isError:       true,
	errorContains: "nope",
}}

func TestGrep(t *testing.T) {
	if len(grepCases) == 0 {
		t.Fatal("the case table is empty, so this test proves nothing about either engine")
	}
	for _, engine := range grepEngines() {
		t.Run(engine.name, func(t *testing.T) {
			if engine.skip != "" {
				t.Skip(engine.skip)
			}
			for _, tc := range grepCases {
				t.Run(tc.name, func(t *testing.T) {
					runGrepCase(t, engine, tc)
				})
			}
		})
	}
}

func runGrepCase(t *testing.T, engine grepEngine, tc grepCase) {
	t.Helper()

	ws := newGrepWorkspace(t, tc.files)
	subject := builtin.NewGrep(ws, engine.path)

	res, err := callGrep(t, context.Background(), subject, tc.args)
	if err != nil {
		t.Fatalf("grep(%v) returned the Go error %v; a search it cannot perform is a failed result, "+
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

	details, ok := res.Details.(*builtin.GrepDetails)
	if !ok {
		t.Fatalf("Details is %T, want *builtin.GrepDetails", res.Details)
	}
	// The guard that keeps the ripgrep half of this table honest: a bad binary
	// falls back to the pure-Go engine, and without this the whole run would pass
	// while testing one engine twice.
	if details.Engine != engine.name {
		t.Fatalf("Details.Engine = %q, want %q: the engine under test is not the one that answered",
			details.Engine, engine.name)
	}

	want := tc.want
	if diff := grepMatchDiff(details.Matches, want); diff != "" {
		t.Errorf("matches:\n%s", diff)
	}
	total := tc.total
	if total == 0 {
		total = len(want)
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
		checkGrepListing(t, res.Content, want, total)
	}
}

// checkGrepListing holds the model-visible contract: one path:line:text line per
// match, in the order Details reports them, and a truncation notice exactly when
// there were more.
func checkGrepListing(t *testing.T, content string, want []builtin.GrepMatch, total int) {
	t.Helper()

	listing, notice, _ := strings.Cut(content, "\n\n")
	lines := strings.Split(strings.TrimRight(listing, "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("Content has %d listed lines, want %d:\n%s", len(lines), len(want), content)
	}
	for i, m := range want {
		if got := fmt.Sprintf("%s:%d:%s", m.Path, m.Line, m.Text); lines[i] != got {
			t.Errorf("Content line %d = %q, want %q", i+1, lines[i], got)
		}
	}

	switch truncated := total > len(want); {
	case truncated && !strings.Contains(notice, fmt.Sprintf("first %d of %d matches", len(want), total)):
		t.Errorf("Content does not say how many matches were left out:\n%s", content)
	case !truncated && strings.TrimSpace(notice) != "":
		t.Errorf("Content carries a truncation notice for a complete result:\n%s", content)
	}
}

// TestGrepDoesNotFollowSymlinks covers both kinds, because only one of them
// tests this tool. A link out of the workspace is refused by the workspace
// itself whatever grep does; a link that stays inside opens perfectly well, and
// following it would report the same file twice under two names while ripgrep,
// which needs -L to follow anything, reported it once.
func TestGrepDoesNotFollowSymlinks(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "ws")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{dir, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("creating %s: %v", d, err)
		}
	}
	writeGrepFile(t, outside, "secret.txt", "target outside\n")
	writeGrepFile(t, dir, "a.txt", "target inside\n")
	writeGrepFile(t, dir, "sub/b.txt", "target in the subtree\n")

	// Relative targets, the way a repository writes them, and the only form that
	// reaches this tool: os.Root refuses a symlink with an absolute target
	// outright, so absolute links here would be testing the workspace instead.
	links := map[string]string{
		"out.txt": "../outside/secret.txt",
		"outdir":  "../outside",
		"dup.txt": "a.txt",
		"dupdir":  "sub",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Skipf("this platform will not create the symlink %s: %v", name, err)
		}
	}

	ws := openGrepWorkspace(t, dir)
	want := []builtin.GrepMatch{
		{Path: "a.txt", Line: 1, Text: "target inside"},
		{Path: "sub/b.txt", Line: 1, Text: "target in the subtree"},
	}
	for _, engine := range grepEngines() {
		t.Run(engine.name, func(t *testing.T) {
			if engine.skip != "" {
				t.Skip(engine.skip)
			}
			res, err := callGrep(t, context.Background(), builtin.NewGrep(ws, engine.path),
				map[string]any{"pattern": "target"})
			if err != nil {
				t.Fatalf("grep returned the Go error %v", err)
			}
			details, ok := res.Details.(*builtin.GrepDetails)
			if !ok {
				t.Fatalf("Details is %T, want *builtin.GrepDetails", res.Details)
			}
			if diff := grepMatchDiff(details.Matches, want); diff != "" {
				t.Errorf("a symlink was followed:\n%s", diff)
			}
		})
	}
}

// TestGrepIgnoresARipgrepConfigFile guards the one input to ripgrep that is not
// a flag: RIPGREP_CONFIG_PATH names a file of extra arguments, so a developer
// who has one would get different search results from the same rasp.
func TestGrepIgnoresARipgrepConfigFile(t *testing.T) {
	config := filepath.Join(t.TempDir(), "ripgreprc")
	if err := os.WriteFile(config, []byte("--glob=!a.txt\n"), 0o644); err != nil {
		t.Fatalf("writing the ripgrep config: %v", err)
	}
	t.Setenv("RIPGREP_CONFIG_PATH", config)

	ws := newGrepWorkspace(t, map[string]string{"a.txt": "target here\n"})
	want := []builtin.GrepMatch{{Path: "a.txt", Line: 1, Text: "target here"}}
	for _, engine := range grepEngines() {
		t.Run(engine.name, func(t *testing.T) {
			if engine.skip != "" {
				t.Skip(engine.skip)
			}
			res, err := callGrep(t, context.Background(), builtin.NewGrep(ws, engine.path),
				map[string]any{"pattern": "target"})
			if err != nil {
				t.Fatalf("grep returned the Go error %v", err)
			}
			details, ok := res.Details.(*builtin.GrepDetails)
			if !ok {
				t.Fatalf("Details is %T, want *builtin.GrepDetails; content was %q", res.Details, res.Content)
			}
			if diff := grepMatchDiff(details.Matches, want); diff != "" {
				t.Errorf("a config file outside rasp changed the search:\n%s", diff)
			}
		})
	}
}

// TestGrepFallsBackWhenRipgrepCannotStart covers the degraded mode design §12
// promises, and pins the reason Details names an engine at all: a configured
// ripgrep that will not run is not a failed search.
func TestGrepFallsBackWhenRipgrepCannotStart(t *testing.T) {
	ws := newGrepWorkspace(t, map[string]string{"a.txt": "target here\n"})
	missing := filepath.Join(t.TempDir(), "not-ripgrep")

	res, err := callGrep(t, context.Background(), builtin.NewGrep(ws, missing),
		map[string]any{"pattern": "target"})
	if err != nil {
		t.Fatalf("grep returned the Go error %v", err)
	}
	if res.IsError {
		t.Fatalf("a missing ripgrep failed the search instead of falling back:\n%s", res.Content)
	}
	details, ok := res.Details.(*builtin.GrepDetails)
	if !ok {
		t.Fatalf("Details is %T, want *builtin.GrepDetails", res.Details)
	}
	if details.Engine != builtin.GrepEngineGo {
		t.Errorf("Details.Engine = %q, want %q", details.Engine, builtin.GrepEngineGo)
	}
	want := []builtin.GrepMatch{{Path: "a.txt", Line: 1, Text: "target here"}}
	if diff := grepMatchDiff(details.Matches, want); diff != "" {
		t.Errorf("matches:\n%s", diff)
	}
}

func TestGrepReportsAnInterruptedTurnAsAGoError(t *testing.T) {
	ws := newGrepWorkspace(t, map[string]string{"a.txt": "target here\n"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, engine := range grepEngines() {
		t.Run(engine.name, func(t *testing.T) {
			if engine.skip != "" {
				t.Skip(engine.skip)
			}
			res, err := builtin.NewGrep(ws, engine.path).Run(ctx, grepArgs(t, map[string]any{"pattern": "target"}))
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled: a torn-down turn is the tool failing to "+
					"run, not an observation the model can act on (design §12). Result was %+v", err, res)
			}
		})
	}
}

func TestGrepNeedsAWorkspace(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewGrep(nil, \"\") returned a tool that has no route to the filesystem")
		}
	}()
	builtin.NewGrep(nil, "")
}

// grepMatchDiff describes the first difference between two match lists, or "".
func grepMatchDiff(got, want []builtin.GrepMatch) string {
	for i := range max(len(got), len(want)) {
		switch {
		case i >= len(got):
			return fmt.Sprintf("missing match %d: %+v", i, want[i])
		case i >= len(want):
			return fmt.Sprintf("unexpected match %d: %+v", i, got[i])
		case got[i] != want[i]:
			return fmt.Sprintf("match %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
	return ""
}

func newGrepWorkspace(t *testing.T, files map[string]string) *workspace.Workspace {
	t.Helper()

	base := t.TempDir()
	dir := filepath.Join(base, "ws")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	// A directory beside the workspace, so ../outside names something real and the
	// refusal is confinement rather than a missing path.
	writeGrepFile(t, base, "outside/secret.txt", "target outside\n")
	// An ignore file one level above every workspace. ripgrep reads those by
	// default and walks up to /, so every case in the table runs against a tree
	// where honouring it would show.
	writeGrepFile(t, base, ".gitignore", "hidden-by-parent.txt\n")
	for name, content := range files {
		writeGrepFile(t, dir, name, content)
	}
	return openGrepWorkspace(t, dir)
}

func openGrepWorkspace(t *testing.T, dir string) *workspace.Workspace {
	t.Helper()

	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatalf("opening the workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := ws.Close(); err != nil {
			t.Errorf("closing the workspace: %v", err)
		}
	})
	return ws
}

func writeGrepFile(t *testing.T, dir, name, content string) {
	t.Helper()

	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("creating the parent of %s: %v", name, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// callGrep runs the tool the way the loop does, through the JSON the model sends.
func callGrep(t *testing.T, ctx context.Context, subject tool.Tool, args map[string]any) (tool.Result, error) {
	t.Helper()
	return subject.Run(ctx, grepArgs(t, args))
}

func grepArgs(t *testing.T, args map[string]any) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encoding %v: %v", args, err)
	}
	return raw
}

func manyLines(n int) map[string]string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "target %d\n", i)
	}
	return map[string]string{"many.txt": b.String()}
}

func manyLinesWant(n int) []builtin.GrepMatch {
	want := make([]builtin.GrepMatch, 0, n)
	for i := 1; i <= n; i++ {
		want = append(want, builtin.GrepMatch{Path: "many.txt", Line: i, Text: fmt.Sprintf("target %d", i)})
	}
	return want
}

func manyFiles(n int) map[string]string {
	files := make(map[string]string, n)
	for i := 1; i <= n; i++ {
		files[fmt.Sprintf("f%03d.txt", i)] = fmt.Sprintf("target %d\n", i)
	}
	return files
}

func manyFilesWant(n int) []builtin.GrepMatch {
	want := make([]builtin.GrepMatch, 0, n)
	for i := 1; i <= n; i++ {
		want = append(want, builtin.GrepMatch{
			Path: fmt.Sprintf("f%03d.txt", i),
			Line: 1,
			Text: fmt.Sprintf("target %d", i),
		})
	}
	return want
}
