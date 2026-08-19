package builtin

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/theocod3s/rasp/internal/tool"
)

const (
	// maxGrepMatches is how many matches one call returns. Past that the model is
	// holding a list it cannot act on; the exact total comes back regardless, so
	// "narrow the pattern" is a decision it can make rather than guess at.
	maxGrepMatches = 100

	// maxGrepLine clips one matching line. A minified bundle is a single line of
	// a hundred kilobytes, and a hundred of those is the whole context window.
	maxGrepLine = 512

	// maxGrepScan bounds a single line in the pure-Go engine, which has to hold
	// one in memory to match it. A file with a longer line is treated the way a
	// binary file is; ripgrep streams and would still search it, so this is the
	// one place the two engines can disagree.
	maxGrepScan = 4 << 20

	// maxGrepStderr is how much of ripgrep's diagnostics we keep. A tree full of
	// unreadable files produces one line each, and none of them is the answer.
	maxGrepStderr = 4 << 10
)

// Which engine produced a result. The UI reads it; so does a test that needs to
// know the engine it asked for is the one that ran.
const (
	GrepEngineRipgrep = "ripgrep"
	GrepEngineGo      = "go"
)

// ripgrepArgs pins every input to ripgrep's file selection except the workspace
// itself, so the two engines choose the same files to search. Each flag is here
// because its default would diverge:
//
//   - --hidden: dotfiles are ordinary source (.github/, .env). git does not hide
//     them and neither does the fallback.
//   - --glob=!.git: --hidden otherwise walks into the object store.
//   - --no-require-git: ripgrep applies .gitignore only inside a git repository
//     by default, so without this a workspace that is not one silently stops
//     honouring it.
//   - --no-ignore-dot, --no-ignore-exclude: .ignore, .rgignore and
//     .git/info/exclude are sources the fallback has no notion of.
//   - --no-ignore-global: the user's core.excludesFile makes results depend on
//     the machine.
//   - --no-ignore-parent: ignore files above the searched path are outside the
//     workspace's reach, and ripgrep would walk up to /.
//   - --encoding=none: ripgrep transcodes UTF-16 by default and would search a
//     file the null-byte rule below calls binary.
var ripgrepArgs = []string{
	"--json",
	"--hidden",
	"--glob=!.git",
	"--no-require-git",
	"--no-ignore-dot",
	"--no-ignore-exclude",
	"--no-ignore-global",
	"--no-ignore-parent",
	"--encoding=none",
}

const grepDescription = `Search file contents for a regular expression and return each matching line as
path:line:text.

The pattern is an RE2 regular expression — no backreferences and no lookaround. Escape any
character you mean literally. Prefix it with (?i) to match case-insensitively.

Files ignored by a .gitignore in the searched tree are skipped, as is anything that looks
binary. Results are capped, so narrow the pattern or pass path to search one subtree when the
count comes back large.`

type grepInput struct {
	Pattern string `json:"pattern"        desc:"RE2 regular expression to search for. Prefix with (?i) for a case-insensitive search."`
	Path    string `json:"path,omitempty" desc:"File or directory to search, relative to the workspace root or absolute. Defaults to the whole workspace."`
}

// GrepMatch is one matching line, and is the same value whichever engine found
// it.
type GrepMatch struct {
	Path string // workspace-relative, slash-separated
	Line int    // counting from 1
	Text string // the line, newline stripped and clipped to maxGrepLine
}

// GrepDetails is the search's payload for the UI, which the model never sees.
type GrepDetails struct {
	Pattern string
	Path    string // the searched path, workspace-relative
	Engine  string
	Matches []GrepMatch // sorted by path then line, at most maxGrepMatches of them
	Total   int         // every match found, which may exceed len(Matches)
}

type grepFS interface {
	Root() string
	Resolve(name string) (string, error)
	Stat(name string) (fs.FileInfo, error)
	FS() fs.FS
}

// RipgrepPath is the ripgrep binary to search with, or "" when there is none on
// PATH and the pure-Go engine has to do it.
func RipgrepPath() string {
	rg, err := exec.LookPath("rg")
	if err != nil {
		return ""
	}
	return rg
}

// NewGrep returns the grep tool. It searches with the ripgrep binary at rg, or
// with the pure-Go engine when that is empty — passed in rather than looked up
// here so a test can exercise either engine on a host that happens to have or
// lack ripgrep (design §14, prd §6.2).
func NewGrep(ws grepFS, rg string) tool.Tool {
	if ws == nil {
		panic("builtin: grep needs a workspace, which is the only route a file tool has to the filesystem")
	}
	g := &grepTool{ws: ws, ripgrep: rg}
	return tool.New("grep", grepDescription, g.run)
}

type grepTool struct {
	ws      grepFS
	ripgrep string
}

func (g *grepTool) run(ctx context.Context, in grepInput) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if in.Pattern == "" {
		return grepFailed("grep was called with no pattern to search for."), nil
	}
	// Compiled here rather than left to whichever engine runs, so a bad pattern is
	// refused in the same words on both, and one Go rejects cannot quietly work
	// only on the machines that have ripgrep.
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return grepFailed("grep %q: %v", in.Pattern, err), nil
	}

	target := in.Path
	if target == "" {
		target = "."
	}
	// ripgrep reports a path that is not there on stderr and still exits 0, so the
	// existence check belongs here for the two engines to refuse alike.
	info, err := g.ws.Stat(target)
	if err != nil {
		return grepFailed("%v", err), nil
	}
	rel, err := g.ws.Resolve(target)
	if err != nil {
		return grepFailed("%v", err), nil
	}

	c := &grepCollector{}
	engine := GrepEngineGo
	if g.ripgrep != "" {
		ran, fail, err := g.searchRipgrep(ctx, rel, in.Pattern, c)
		switch {
		case err != nil:
			return tool.Result{}, err
		case fail != nil:
			return *fail, nil
		case ran:
			engine = GrepEngineRipgrep
		}
	}
	if engine == GrepEngineGo {
		if err := g.searchGo(ctx, rel, info.IsDir(), re, c); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				// The turn is being torn down, which is the tool failing to run rather
				// than an observation the model can act on (design §12).
				return tool.Result{}, err
			}
			return grepFailed("grep %q: %v", rel, err), nil
		}
	}

	return grepResult(in.Pattern, rel, engine, c), nil
}

// searchRipgrep runs ripgrep into c. ran is false only when ripgrep could not be
// started at all — the one failure the pure-Go engine can cover for, and the
// reason Details names the engine that actually answered.
func (g *grepTool) searchRipgrep(ctx context.Context, rel, pattern string, c *grepCollector) (ran bool, fail *tool.Result, err error) {
	args := append(slices.Clone(ripgrepArgs), "--regexp", pattern, "--", filepath.FromSlash(rel))
	cmd := exec.CommandContext(ctx, g.ripgrep, args...)
	// Relative to the workspace root, so the paths ripgrep prints are already the
	// workspace-relative ones the model gets back. ripgrep does not follow
	// symlinks, so this stays inside the confinement rel was resolved against.
	cmd.Dir = g.ws.Root()
	cmd.Env = envWithoutRipgrepConfig()
	stderr := &grepStderr{}
	cmd.Stderr = stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, nil, nil
	}
	if err := cmd.Start(); err != nil {
		return false, nil, nil
	}

	decodeErr := decodeRipgrep(stdout, c)
	// Drained before Wait either way: os/exec closes the pipe in Wait, and a child
	// blocked writing into a pipe nobody reads would never exit.
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()

	if err := ctx.Err(); err != nil {
		return true, nil, err
	}
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	switch {
	case decodeErr != nil:
		res := grepFailed("grep: cannot read ripgrep's output: %v", decodeErr)
		return true, &res, nil
	// Exit 1 is "no matches" and exit 2 is "something went wrong", which ripgrep
	// also returns for a single unreadable file in an otherwise fine tree. Only a
	// run that produced nothing at all is reported as a failure.
	case code >= 2 && c.total == 0:
		res := grepFailed("grep: ripgrep exited %d: %s", code, stderr.String())
		return true, &res, nil
	case code < 0:
		res := grepFailed("grep: ripgrep did not run: %v", waitErr)
		return true, &res, nil
	}
	return true, nil, nil
}

// envWithoutRipgrepConfig drops RIPGREP_CONFIG_PATH from the child's
// environment. ripgrep reads that file for extra flags, so a user with one
// configured would get different results from the two engines, and different
// results from the same rasp on another machine.
func envWithoutRipgrepConfig() []string {
	env := os.Environ()
	return slices.DeleteFunc(env, func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return strings.EqualFold(name, "RIPGREP_CONFIG_PATH")
	})
}

// ripgrepEvent is the part of ripgrep's --json stream this tool reads. Records
// for one file arrive together, bracketed by begin and end, which is what lets
// end retract the whole file.
type ripgrepEvent struct {
	Type string `json:"type"`
	Data struct {
		Path       *ripgrepText `json:"path"`
		Lines      *ripgrepText `json:"lines"`
		LineNumber int          `json:"line_number"`

		// Non-nil on end when ripgrep found a null byte. A directory search never
		// reports such a file at all, but a file named directly gets searched as
		// text, so the matches are dropped here to keep the null-byte rule one rule.
		BinaryOffset *int `json:"binary_offset"`
	} `json:"data"`
}

// ripgrepText carries text only when the bytes were valid UTF-8; otherwise
// ripgrep sends a base64 "bytes" field instead, and a path we cannot name is a
// path the model cannot read.
type ripgrepText struct {
	Text string `json:"text"`
}

func decodeRipgrep(r io.Reader, c *grepCollector) error {
	dec := json.NewDecoder(r)
	var file grepFile
	for {
		var ev ripgrepEvent
		switch err := dec.Decode(&ev); {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return err
		}

		switch ev.Type {
		case "begin":
			file = grepFile{}
		case "match":
			if ev.Data.Path == nil || ev.Data.Lines == nil || ev.Data.Path.Text == "" {
				continue
			}
			file.add(GrepMatch{
				Path: path.Clean(filepath.ToSlash(ev.Data.Path.Text)),
				Line: ev.Data.LineNumber,
				Text: clipGrepLine(ev.Data.Lines.Text),
			})
		case "end":
			if ev.Data.BinaryOffset == nil {
				c.addFile(file)
			}
			file = grepFile{}
		}
	}
}

func (g *grepTool) searchGo(ctx context.Context, rel string, isDir bool, re *regexp.Regexp, c *grepCollector) error {
	fsys := g.ws.FS()
	if !isDir {
		// A file named directly is searched whether or not a .gitignore covers it,
		// which is what ripgrep does with an explicit argument.
		file, err := searchGrepFile(fsys, rel, re)
		if err != nil {
			return err
		}
		c.addFile(file)
		return nil
	}

	ig := newIgnorer(rel)
	return fs.WalkDir(fsys, rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory is skipped rather than fatal, which is what
			// ripgrep does with one: a search of the tree should not fail on a
			// corner of it the model was not asking about.
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		switch {
		// The object store is not source, and --hidden would otherwise walk it.
		case p != rel && d.Name() == ".git":
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		case d.IsDir():
			if p != rel && ig.ignored(p, true) {
				return fs.SkipDir
			}
			ig.load(fsys, p)
			return nil
		// Symlinks are what this excludes that matters. ripgrep needs -L to follow
		// one, so a link to a file already inside the workspace must not come back
		// a second time under its other name; a link out of the workspace the root
		// refuses anyway. Devices and sockets fall out of the same check.
		case !d.Type().IsRegular():
			return nil
		case ig.ignored(p, false):
			return nil
		}

		file, err := searchGrepFile(fsys, p, re)
		if err != nil {
			return err
		}
		c.addFile(file)
		return nil
	})
}

// searchGrepFile scans one file. A null byte anywhere in it retracts every match
// found, because that is what ripgrep does when it hits one: the file is binary
// and none of it was text.
func searchGrepFile(fsys fs.FS, p string, re *regexp.Regexp) (grepFile, error) {
	f, err := fsys.Open(p)
	if err != nil {
		// Unreadable for the same reason a directory can be, and skipped the same way.
		return grepFile{}, nil
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(nil, maxGrepScan)

	var file grepFile
	for line := 1; sc.Scan(); line++ {
		text := sc.Bytes()
		if bytes.IndexByte(text, 0) >= 0 {
			return grepFile{}, nil
		}
		if re.Match(text) {
			file.add(GrepMatch{Path: p, Line: line, Text: clipGrepLine(string(text))})
		}
	}
	if sc.Err() != nil {
		return grepFile{}, nil
	}
	return file, nil
}

// grepFile stages one file's matches so a null byte discovered at the end can
// still retract them. Matches arrive in line order, so the ones past the cap are
// the ones a whole-search cap would drop anyway.
type grepFile struct {
	matches []GrepMatch
	total   int
}

func (f *grepFile) add(m GrepMatch) {
	f.total++
	if len(f.matches) < maxGrepMatches {
		f.matches = append(f.matches, m)
	}
}

// grepCollector keeps the maxGrepMatches lowest matches by path and line, and
// counts the rest. Ordering it here is what makes the two engines agree: the
// pure-Go walk is alphabetical and ripgrep's is whatever its threads finish in,
// so a cap applied in arrival order would return a different hundred matches
// depending on which engine ran.
type grepCollector struct {
	matches []GrepMatch
	total   int
}

func (c *grepCollector) addFile(f grepFile) {
	c.total += f.total
	for _, m := range f.matches {
		i, _ := slices.BinarySearchFunc(c.matches, m, compareGrepMatch)
		if i >= maxGrepMatches {
			continue
		}
		c.matches = slices.Insert(c.matches, i, m)
		if len(c.matches) > maxGrepMatches {
			c.matches = c.matches[:maxGrepMatches]
		}
	}
}

func compareGrepMatch(a, b GrepMatch) int {
	return cmp.Or(strings.Compare(a.Path, b.Path), cmp.Compare(a.Line, b.Line))
}

func grepResult(pattern, rel, engine string, c *grepCollector) tool.Result {
	details := &GrepDetails{Pattern: pattern, Path: rel, Engine: engine, Matches: c.matches, Total: c.total}
	if c.total == 0 {
		where := "the workspace"
		if rel != "." {
			where = rel
		}
		return tool.Result{
			Content: fmt.Sprintf("No matches for %q in %s.", pattern, where),
			Title:   fmt.Sprintf("grep %s (no matches)", pattern),
			Details: details,
		}
	}

	var b strings.Builder
	for _, m := range c.matches {
		fmt.Fprintf(&b, "%s:%d:%s\n", m.Path, m.Line, m.Text)
	}
	if c.total > len(c.matches) {
		fmt.Fprintf(&b, "\nShowing the first %d of %d matches, in path order. Narrow the pattern, "+
			"or pass path to search one subtree.\n", len(c.matches), c.total)
	}
	return tool.Result{
		Content: b.String(),
		Title:   grepTitle(pattern, c),
		Details: details,
	}
}

func grepTitle(pattern string, c *grepCollector) string {
	files := 0
	for i, m := range c.matches {
		if i == 0 || m.Path != c.matches[i-1].Path {
			files++
		}
	}
	return fmt.Sprintf("grep %s (%s in %s)", pattern,
		grepCount(c.total, "match", "matches"), grepCount(files, "file", "files"))
}

func grepCount(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func clipGrepLine(s string) string {
	s = strings.TrimRight(s, "\r\n")
	if len(s) <= maxGrepLine {
		return s
	}
	cut := maxGrepLine
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func grepFailed(format string, a ...any) tool.Result {
	return tool.Result{IsError: true, Content: fmt.Sprintf(format, a...)}
}

// ignorer applies the .gitignore files inside the searched tree. It implements a
// deliberate subset of git's format, chosen to cover what real repositories
// write without pulling in a dependency: comments and blank lines, a leading !
// to re-include, a leading or interior / to anchor to the file's own directory,
// a trailing / to match directories only, ** to span any number of path
// segments, and *, ? and [...] inside a segment. Later files win over shallower
// ones and, within a file, the last matching pattern wins — git's precedence.
//
// Not implemented: backslash escapes (\#, \!, a trailing "\ "), and the ignore
// sources that are not a .gitignore in the tree — .git/info/exclude, the user's
// core.excludesFile, and ripgrep's own .ignore and .rgignore. ripgrep is told to
// skip all of those (see ripgrepArgs), so the two engines agree on the subset
// rather than one of them being richer.
//
// Files above the searched path are not read either, which means grepping a
// subdirectory does not see the workspace-root .gitignore. That is
// --no-ignore-parent's behaviour, and it is the flag's reason: ripgrep would
// otherwise walk up past the workspace root entirely.
type ignorer struct {
	base string
	dirs map[string][]ignoreRule
}

type ignoreRule struct {
	segs     []string
	negate   bool
	dirOnly  bool
	anchored bool
}

func newIgnorer(base string) *ignorer {
	return &ignorer{base: base, dirs: map[string][]ignoreRule{}}
}

func (ig *ignorer) load(fsys fs.FS, dir string) {
	data, err := fs.ReadFile(fsys, path.Join(dir, ".gitignore"))
	if err != nil {
		return
	}
	var rules []ignoreRule
	for line := range strings.Lines(string(data)) {
		if rule, ok := parseIgnoreRule(line); ok {
			rules = append(rules, rule)
		}
	}
	if len(rules) > 0 {
		ig.dirs[dir] = rules
	}
}

func (ig *ignorer) ignored(p string, isDir bool) bool {
	for dir := path.Dir(p); ; dir = path.Dir(dir) {
		if rules, ok := ig.dirs[dir]; ok {
			rel := p
			if dir != "." {
				rel = p[len(dir)+1:]
			}
			segs := strings.Split(rel, "/")
			for _, rule := range slices.Backward(rules) {
				if rule.match(segs, isDir) {
					return !rule.negate
				}
			}
		}
		if dir == ig.base || dir == "." {
			return false
		}
	}
}

func parseIgnoreRule(line string) (ignoreRule, bool) {
	line = strings.TrimRight(line, " \t\r\n")
	if line == "" || strings.HasPrefix(line, "#") {
		return ignoreRule{}, false
	}

	var rule ignoreRule
	if rule.negate = strings.HasPrefix(line, "!"); rule.negate {
		line = line[1:]
	}
	if rule.dirOnly = strings.HasSuffix(line, "/"); rule.dirOnly {
		line = strings.TrimSuffix(line, "/")
	}
	// A separator anywhere but the end anchors the pattern to the .gitignore's own
	// directory; without one it matches a name at any depth below it.
	rule.anchored = strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")
	if line == "" {
		return ignoreRule{}, false
	}
	rule.segs = strings.Split(line, "/")
	return rule, true
}

func (r ignoreRule) match(segs []string, isDir bool) bool {
	if r.dirOnly && !isDir {
		return false
	}
	if r.anchored {
		return matchIgnoreSegs(r.segs, segs)
	}
	for i := range segs {
		if matchIgnoreSegs(r.segs, segs[i:]) {
			return true
		}
	}
	return false
}

func matchIgnoreSegs(pat, segs []string) bool {
	switch {
	case len(pat) == 0:
		return len(segs) == 0
	case pat[0] == "**":
		for i := 0; i <= len(segs); i++ {
			if matchIgnoreSegs(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	case len(segs) == 0:
		return false
	}
	// A malformed bracket expression comes back as ErrBadPattern, which is a line
	// in someone's .gitignore we cannot honour rather than a search that fails.
	ok, err := path.Match(pat[0], segs[0])
	if err != nil || !ok {
		return false
	}
	return matchIgnoreSegs(pat[1:], segs[1:])
}

// grepStderr keeps the first maxGrepStderr bytes ripgrep writes to stderr and
// drops the rest, so a tree full of unreadable files cannot grow the buffer
// without bound.
type grepStderr struct{ buf []byte }

func (w *grepStderr) Write(p []byte) (int, error) {
	if room := maxGrepStderr - len(w.buf); room > 0 {
		w.buf = append(w.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (w *grepStderr) String() string { return strings.TrimSpace(string(w.buf)) }
