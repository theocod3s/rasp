package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// commandTTL is how long a `$(command)` result is reused. prd §6.1 wants
	// credentials re-resolved on every model call, so that an expiring token
	// recovers on its own rather than killing a turn — but `$(op read …)`
	// forks a process and can take a third of a second, and paying that per
	// request would be absurd. Half a minute is short enough that a rotated
	// credential comes right without a restart.
	commandTTL = 30 * time.Second

	// commandTimeout bounds one command. A vault that is unreachable, or a
	// helper waiting on a prompt nobody can see, must fail rather than hang
	// the turn behind it.
	commandTimeout = 10 * time.Second

	// commandWaitDelay is how long the pipes are given to close after the
	// deadline passes.
	//
	// Without it the deadline does not bind at all. Cancelling the context
	// kills the shell, but not whatever the shell left running, and reading
	// stdout waits for every writer to let go of it — so `$(pass show key)`,
	// where pass starts a gpg-agent that outlives it, blocks for as long as
	// the agent lives and then reports success. Credentials are re-resolved
	// every model call, so that is every turn.
	commandWaitDelay = time.Second
)

// Expander resolves the shell forms in a config value: the environment
// references and the `$(command)` substitutions design §10 defines.
//
// It is separate from Load because the two answer different questions at
// different times. Load reads files once and resolves precedence; Expander
// runs on every model call, because a credential can expire mid-session and
// the value in the file is a recipe rather than a secret.
//
// It is safe for concurrent use.
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

// ExpanderOptions replaces the pieces of the world an Expander touches. The
// zero value uses the real environment, a real subprocess and the real clock.
type ExpanderOptions struct {
	Getenv func(string) (string, bool)

	// Run executes one command and returns its stdout. It is given a context
	// already bounded by commandTimeout.
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

// Expand resolves the value at a key path.
//
// design §10 sends the two secret-bearing shapes through this —
// `providers.*.api_key` and `mcp.servers.*.env.*`, the pair IsSecret names —
// but nothing here is specific to them; the caller decides what to resolve.
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
	if !needsExpansion(value) {
		return value, nil
	}

	fail := func(err error) (string, error) {
		return "", &ExpandError{Key: key, Origin: e.origins[key], Err: err}
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
// strict substring of the one above, so this cannot be reached by a config
// anyone wrote on purpose — it is here because the input is a file and a
// crash is a worse answer than an error.
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

// expandVar resolves one environment reference.
//
// A variable that is set but empty counts as unset, which is both the shell's
// rule for the `:` operators and the rule the environment layer already
// applies when it decides whether a variable contributed anything.
func (e *Expander) expandVar(ctx context.Context, key string, seg segment, depth int) (string, error) {
	value, present := e.getenv(seg.text)
	if present && value != "" {
		return value, nil
	}

	switch seg.op {
	case '-':
		// The default is a value in its own right, not text: `${A:-${B}}`
		// means "A, or B", and `${A:-$(op read …)}` means "A, or ask the
		// vault". Passing it through unexpanded would hand the caller the
		// nine characters `${B}` as a credential — the exact failure this
		// package refuses `${VAR:=x}` to avoid.
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
		// A bare reference to an unset variable is an error rather than the
		// empty string the shell would give. Every value that reaches this
		// resolver is a credential or a server's environment, and an empty
		// one is never what anyone meant — it comes back later as a 401
		// pointing at nothing, which is the failure `${VAR:?msg}` exists to
		// turn into a sentence. Write `${VAR:-}` to ask for empty on purpose.
		return "", unsetError(seg.text, present)
	}
}

// unsetError is what an unset variable says when the config gave no message of
// its own — `${VAR:?}` is legal and carries none, and an error rendering as a
// bare colon tells the reader nothing.
//
// Set-but-empty gets its own sentence. The two are the same to the operators,
// which is the shell's rule, but they are not the same to the reader: someone
// with `export ANTHROPIC_API_KEY=""` in their profile who is told the variable
// is not set will go and look, find it, and have nowhere left to go.
func unsetError(name string, present bool) error {
	if present {
		return fmt.Errorf("$%s is set but empty; write ${%s:-} to accept an empty value", name, name)
	}
	return fmt.Errorf("$%s is not set in the environment", name)
}

// expandCommand runs one `$(command)`, or refuses to.
func (e *Expander) expandCommand(ctx context.Context, key, command string) (string, error) {
	origin, known := e.origins.At(key)
	switch {
	case !known:
		// A guard whose signal is missing must refuse, not proceed. The zero
		// Origin reads as LayerDefault, so looking the layer up directly
		// would have let a command run precisely when nothing could say where
		// it came from (AGENTS.md: a check that cannot run must fail).
		return "", fmt.Errorf(
			"refusing to run %s: nothing records which config set this value, and a command "+
				"that may have arrived with a repository is not one to run on a guess",
			strconv.Quote("$("+command+")"))

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

	// Trimmed, because every credential helper worth using prints a trailing
	// newline and no API accepts one.
	value := strings.TrimSpace(string(out))

	// Succeeding while printing nothing is the command form of the empty
	// variable this resolver already refuses, and it is the likelier of the
	// two: `op read` on an empty field exits 0, and so does a helper that
	// takes a path where it writes no output.
	if value == "" {
		return "", fmt.Errorf("$(%s) succeeded but printed nothing", command)
	}

	// Only successes are cached. A failure is usually a locked vault or a
	// helper that is not signed in, and both are fixed while rasp is running —
	// caching the failure would make the fix look like it did not work.
	e.mu.Lock()
	e.cache[command] = cacheEntry{value: value, expires: e.now().Add(commandTTL)}
	e.mu.Unlock()

	return value, nil
}

// cached returns a result that has not expired.
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
// that `$(cat secret | head -1)` means what it looks like.
func runCommand(ctx context.Context, command string) ([]byte, error) {
	cmd := shellCommand(ctx, command)
	cmd.WaitDelay = commandWaitDelay
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}

	// The exit status alone is not actionable — `exit status 1` from `op` says
	// nothing, while its stderr says "you are not currently signed in".
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return nil, fmt.Errorf("%w: %s", err, msg)
	}
	return nil, err
}

// ExpandError is a config value that could not be resolved. It names the key
// and where that key came from, because "which of my four config sources set
// this" is the question a failure here raises.
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

// displayKind names what arrived, for an error about a value that cannot be a
// string.
func displayKind(val any) string {
	kind, _ := kindOf(val)
	return kind.String()
}

// globalConfigHint names the global config file for an error message, falling
// back to its documented location when the real path cannot be derived.
func globalConfigHint() string {
	if path, err := GlobalPath(); err == nil {
		return path
	}
	return "~/.config/rasp/" + File
}
