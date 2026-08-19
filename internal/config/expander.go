package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// commandTTL is how long a `$(command)` result is reused. The ideal is a
	// credential re-resolved on every model call, but `$(op read …)` forks a
	// process and can take a third of a second; half a minute still picks up a
	// rotated credential without a restart.
	commandTTL = 30 * time.Second

	// commandTimeout bounds one command: an unreachable vault, or a helper waiting
	// on a prompt nobody can see, must fail rather than hang the turn.
	commandTimeout = 10 * time.Second

	// commandWaitDelay bounds a child that ignores the kill when its context ends.
	commandWaitDelay = time.Second

	// drainDelay is how long the pipes are read after the command has exited;
	// whatever it wrote is already buffered.
	drainDelay = 200 * time.Millisecond

	// maxStderr caps what a failing command contributes to an error message. The
	// stream is untrusted in size and the message reaches the UI and the log.
	maxStderr = 4 << 10
)

// Expander resolves the shell forms design §10 defines in a config value.
// Separate from Load because Load reads files once while this runs on every model
// call: a credential can expire mid-session, and the value in the file is a
// recipe rather than a secret. Safe for concurrent use.
type Expander struct {
	values  map[string]any
	origins Origins

	getenv func(string) (string, bool)
	run    func(context.Context, string) ([]byte, error)
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	value   string
	expires time.Time
}

// ExpanderOptions replaces the pieces of the world an Expander touches. The zero
// value uses the real environment, subprocess and clock.
type ExpanderOptions struct {
	Getenv func(string) (string, bool)

	// Run is given a context already bounded by commandTimeout.
	Run func(ctx context.Context, command string) ([]byte, error)

	Now func() time.Time
}

// NewExpander returns an Expander over a resolved configuration.
func NewExpander(res *Result, opts ExpanderOptions) *Expander {
	e := &Expander{
		values:  res.Values,
		origins: res.Origins,
		getenv:  opts.Getenv,
		run:     opts.Run,
		now:     opts.Now,
		cache:   map[string]cacheEntry{},
	}
	if e.getenv == nil {
		e.getenv = os.LookupEnv
	}
	if e.run == nil {
		e.run = runCommand
	}
	if e.now == nil {
		e.now = time.Now
	}
	return e
}

// Expand resolves the value at a key path. Nothing here is specific to the
// secret-bearing shapes IsSecret names; the caller decides what to resolve.
func (e *Expander) Expand(ctx context.Context, key string) (string, error) {
	raw, ok := e.values[key]
	if !ok {
		return "", &ExpandError{Key: key, Err: errors.New("no such setting")}
	}
	value, ok := raw.(string)
	if !ok {
		return "", &ExpandError{Key: key, Origin: e.origins[key],
			Err: fmt.Errorf("want a string to expand, got %s", displayKind(raw))}
	}
	fail := func(err error) (string, error) {
		return "", &ExpandError{Key: key, Origin: e.origins[key], Err: err}
	}

	if !needsExpansion(value) {
		return value, nil
	}
	origin, known := e.origins.At(key)
	if !known {
		// A check that cannot run must fail rather than pass (AGENTS.md).
		return fail(errors.New(
			"nothing records which config set this value, so there is no telling whether it " +
				"is a literal or something to resolve"))
	}
	// Only a config file holds a recipe. A value from the environment or a flag
	// has already been through a shell, so running the grammar over it again can
	// only misread a secret that happens to contain a dollar (design §10).
	if !writtenInAFile(origin.Layer) {
		return value, nil
	}

	segs, err := parseValue(value)
	if err != nil {
		return fail(err)
	}
	expanded, err := e.expandSegments(ctx, key, segs, 0)
	if err != nil {
		return fail(err)
	}
	return expanded, nil
}

// maxDepth bounds the recursion through nested defaults. Each level parses a
// strict substring of the one above, so no config written on purpose reaches it;
// the input is a file, and a crash is a worse answer than an error.
const maxDepth = 32

func (e *Expander) expandSegments(ctx context.Context, key string, segs []segment, depth int) (string, error) {
	if depth > maxDepth {
		return "", fmt.Errorf("references nested more than %d deep", maxDepth)
	}

	var out strings.Builder
	for _, seg := range segs {
		switch seg.kind {
		case segLiteral:
			out.WriteString(seg.text)

		case segVar:
			text, err := e.expandVar(ctx, key, seg, depth)
			if err != nil {
				return "", err
			}
			out.WriteString(text)

		case segCommand:
			text, err := e.expandCommand(ctx, key, seg.text)
			if err != nil {
				return "", err
			}
			out.WriteString(text)
		}
	}
	return out.String(), nil
}

// writtenInAFile reports whether a layer is one someone edits by hand, the only
// kind that can hold something worth expanding. Every layer is named rather than
// defaulted, because both wrong answers are silent: a file layer left out returns
// `$(op read …)` to the provider as an API key, and a shell-sourced layer let in
// misreads a key containing a dollar.
func writtenInAFile(l Layer) bool {
	switch l {
	case LayerGlobal, LayerProject:
		return true
	case LayerDefault, LayerEnv, LayerFlag:
		return false
	}
	// Unreachable while every layer is named above, which the compiler does not
	// enforce and TestEveryLayerIsClassified does. The two callers err in opposite
	// directions from here: expandCommand refuses, and Expand hands the value back
	// untouched.
	return false
}

// expandVar resolves one environment reference. Set-but-empty counts as unset,
// which is the shell's rule for the `:` operators.
func (e *Expander) expandVar(ctx context.Context, key string, seg segment, depth int) (string, error) {
	value, present := e.getenv(seg.text)
	if present && value != "" {
		return value, nil
	}

	switch seg.op {
	case '-':
		// The default is a value, not text: `${A:-${B}}` means "A, or B".
		// Passing it through unexpanded would hand the caller the four
		// characters `${B}` as a credential.
		segs, err := parseValue(seg.arg)
		if err != nil {
			return "", fmt.Errorf("in the default for $%s: %w", seg.text, err)
		}
		return e.expandSegments(ctx, key, segs, depth+1)

	case '?':
		if seg.arg == "" {
			return "", unsetError(seg.text, present)
		}
		return "", errors.New(seg.arg)
	default:
		// An error rather than the empty string the shell would give: an empty
		// credential comes back later as a 401 pointing at nothing. `${VAR:-}`
		// asks for empty on purpose.
		return "", unsetError(seg.text, present)
	}
}

// unsetError is what an unset variable says when the config gave no message of
// its own; `${VAR:?}` is legal and carries none. Set-but-empty gets its own
// sentence: someone with `export ANTHROPIC_API_KEY=""` in their profile who is
// told the variable is not set will go and look, find it, and have nowhere left
// to go.
func unsetError(name string, present bool) error {
	if present {
		return fmt.Errorf("$%s is set but empty; write ${%s:-} to accept an empty value", name, name)
	}
	return fmt.Errorf("$%s is not set in the environment; write %s if the dollar is meant literally",
		name, dollarEscape)
}

func (e *Expander) expandCommand(ctx context.Context, key, command string) (string, error) {
	// The first two arms are unreachable through Expand, which refuses an
	// unrecorded origin and returns a shell-sourced value literally long before a
	// command is reached. They stay because what they stop is arbitrary command
	// execution, a guard that depends on its only caller checking first is a
	// comment wearing the clothes of a check, and the MCP server `env` resolution
	// §10 has in scope is a second entry point waiting to happen.
	origin, known := e.origins.At(key)
	switch {
	case !known:
		return "", fmt.Errorf(
			"refusing to run %s: nothing records which config set this value, and a command "+
				"that may have arrived with a repository is not one to run on a guess",
			strconv.Quote("$("+command+")"))

	case !writtenInAFile(origin.Layer):
		return "", fmt.Errorf(
			"refusing to run %s: this value came from the %s, which a shell has already "+
				"resolved — it is a value, not a recipe (design §10)",
			strconv.Quote("$("+command+")"), origin.Layer)

	case origin.Layer == LayerProject:
		return "", fmt.Errorf(
			"refusing to run %s from a project config.\n"+
				"A project config arrives with `git clone`, so running a command from one needs "+
				"your say-so first (design §10). The prompt that asks for it lands with the TUI; "+
				"until then, move this line to your global config at %s, or set the value in the "+
				"environment",
			strconv.Quote("$("+command+")"), globalConfigHint())
	}

	if value, ok := e.cached(command); ok {
		return value, nil
	}

	parent := ctx
	ctx, cancel := context.WithTimeout(parent, commandTimeout)
	defer cancel()

	out, err := e.run(ctx, command)
	if err != nil {
		// The parent is checked first. Both contexts are cancelled when a turn
		// is interrupted, and reporting that as a ten-second timeout would
		// tell a user who pressed Esc that their credential helper is slow.
		if parentErr := parent.Err(); parentErr != nil {
			return "", parentErr
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("$(%s) did not finish within %s", command, commandTimeout)
		}
		return "", fmt.Errorf("$(%s) failed: %w", command, err)
	}

	// Every credential helper prints a trailing newline; no API accepts one.
	value := strings.TrimSpace(string(out))

	// The command form of the empty variable refused above, and the likelier of
	// the two: `op read` on an empty field exits 0.
	if value == "" {
		return "", fmt.Errorf("$(%s) succeeded but printed nothing", command)
	}

	// Only successes. A locked vault is fixed while rasp is running, and caching
	// the failure would make the fix look like it did not work.
	e.mu.Lock()
	e.cache[command] = cacheEntry{value: value, expires: e.now().Add(commandTTL)}
	e.mu.Unlock()

	return value, nil
}

func (e *Expander) cached(command string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	entry, ok := e.cache[command]
	if !ok || !e.now().Before(entry.expires) {
		return "", false
	}
	return entry.value, true
}

// runCommand is the default runner: the command as a shell would read it, so
// `$(cat secret | head -1)` means what it looks like.
//
// The pipes are built here rather than left to cmd.Output because waiting for
// end-of-file on stdout means waiting for every process that inherited it, and a
// credential helper leaving something behind is ordinary — `pass` starts a
// gpg-agent that outlives it by design. Waiting on that hung the turn; forcing
// the pipes shut instead threw the secret away on a race.
func runCommand(ctx context.Context, command string) ([]byte, error) {
	cmd := shellCommand(ctx, command)
	cmd.WaitDelay = commandWaitDelay

	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer outR.Close()

	errR, errW, err := os.Pipe()
	if err != nil {
		outW.Close()
		return nil, err
	}
	defer errR.Close()

	cmd.Stdout, cmd.Stderr = outW, errW

	if err := cmd.Start(); err != nil {
		outW.Close()
		errW.Close()
		return nil, err
	}
	// Ours go now, so a clean exit gives end-of-file straight away.
	outW.Close()
	errW.Close()

	// Alongside the command, not after: a pipe holds about 64KB, and a command
	// that filled it would block forever waiting for a reader.
	out := readPipe(outR)
	msg := readPipe(errR)

	waitErr := cmd.Wait()

	// Anything still holding the write end is something the command left running,
	// and none of it is ours to wait for.
	stdout := out.take()
	stderr := msg.take()

	if waitErr == nil {
		return stdout, nil
	}
	// `exit status 1` from `op` says nothing; its stderr says why.
	if text := strings.TrimSpace(string(stderr)); text != "" {
		return nil, fmt.Errorf("%w: %s", waitErr, truncate(text, maxStderr))
	}
	return nil, waitErr
}

type pending struct {
	file *os.File
	data []byte
	done chan struct{}
}

func readPipe(f *os.File) *pending {
	p := &pending{file: f, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		p.data, _ = io.ReadAll(f)
	}()
	return p
}

// take stops the drain shortly and returns what it read. The deadline releases
// the reader when a lingering process still holds the write end, so the goroutine
// never becomes the leak goleak looks for.
func (p *pending) take() []byte {
	_ = p.file.SetReadDeadline(time.Now().Add(drainDelay))
	<-p.done
	return p.data
}

func truncate(text string, n int) string {
	if len(text) <= n {
		return text
	}
	return text[:n] + "… (truncated)"
}

// ExpandError names the key and where it came from, which is the question a
// failure here raises.
type ExpandError struct {
	Key    string
	Origin Origin
	Err    error
}

func (e *ExpandError) Error() string {
	if e.Origin == (Origin{}) {
		return fmt.Sprintf("%s: %v", e.Key, e.Err)
	}
	return fmt.Sprintf("%s (%s): %v", e.Key, e.Origin, e.Err)
}

func (e *ExpandError) Unwrap() error { return e.Err }

func displayKind(val any) string {
	kind, _ := kindOf(val)
	return kind.String()
}

func globalConfigHint() string {
	if path, err := GlobalPath(); err == nil {
		return path
	}
	return "~/.config/rasp/" + File
}
