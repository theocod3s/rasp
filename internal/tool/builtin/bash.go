package builtin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/theocod3s/rasp/internal/tool"
)

const (
	bashDefaultTimeout = 2 * time.Minute
	bashMaxTimeout     = 10 * time.Minute

	// bashWaitDelay bounds two waits: a command that ignores the kill, and a
	// command that has exited while something it started still holds the output
	// pipe open. Without it the second one waits for that process instead, and
	// `npm run dev &` never lets go of the turn.
	bashWaitDelay = 2 * time.Second
)

const bashDescription = `Run a command with bash, in the directory rasp was started in.

Output is stdout and stderr together, in the order the command wrote them. A non-zero exit is
reported back rather than hidden: read the output and decide what it means. On a timeout or an
interrupted turn the command is killed along with everything it started, so anything meant to
outlive the call has to detach itself.

Prefer the dedicated tools for reading, searching and editing files — their output is structured
and far cheaper.`

// BashInput is one shell call as the model asks for it.
type BashInput struct {
	Command   string `json:"command"              desc:"The command to run, as bash would read it: pipes, redirections and && all mean what they look like."`
	TimeoutMS int    `json:"timeout_ms,omitempty" desc:"How long to let the command run, in milliseconds. Defaults to 120000 and is capped at 600000."`
}

// BashDetails is what the UI draws a shell call from; the model sees none of it.
type BashDetails struct {
	Command  string
	ExitCode int
	Duration time.Duration
}

// Bash runs shell commands. It does not implement Sequential: built-in tools are
// parallel because we audited them (design §6 rule 4), and the commands that
// warrant serializing are the ones the permission gate stops on anyway, which is
// already a serial barrier (§6 rule 5). Declaring it here instead would degrade
// every batch holding a shell call — `go test` beside three reads — to serial.
var Bash = tool.New("bash", bashDescription, runBash)

func runBash(ctx context.Context, in BashInput) (tool.Result, error) {
	if strings.TrimSpace(in.Command) == "" {
		return tool.Result{IsError: true, Content: "bash was called with no command to run."}, nil
	}

	timeout := bashTimeout(in.TimeoutMS)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := &lockedBuffer{}
	cmd := exec.CommandContext(runCtx, "bash", "-c", in.Command)
	// One writer for both streams. The two file descriptors then name one pipe, so
	// the OS interleaves them in the order the command wrote — a compiler error
	// stays beside the file it came from. Draining two pipes separately gives all
	// of stdout and then all of stderr (internals §3.5).
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = bashWaitDelay
	setProcessGroup(cmd)

	started := time.Now()
	err := cmd.Run()

	result := tool.Result{Title: bashTitle(in.Command)}
	details := BashDetails{Command: in.Command, ExitCode: -1, Duration: time.Since(started)}
	if cmd.ProcessState != nil {
		details.ExitCode = cmd.ProcessState.ExitCode()
	}
	result.Details = &details

	var note string
	switch {
	// Cancellation is read from the contexts rather than from err, because a
	// killed command reports the signal that killed it and not why it was sent.
	// The caller's context is checked first: ours is derived from it, so an
	// interrupted turn expires both.
	case ctx.Err() != nil:
		note, result.IsError = "The turn was interrupted, so the command was killed along with everything it started.", true
	case runCtx.Err() != nil:
		note, result.IsError = fmt.Sprintf("The command ran past its %s timeout, so it was killed along with everything it started.", timeout), true
	case cmd.ProcessState == nil:
		note, result.IsError = fmt.Sprintf("bash could not run this command: %v", err), true
	case details.ExitCode != 0:
		note, result.IsError = fmt.Sprintf("exit status %d", details.ExitCode), true
	case errors.Is(err, exec.ErrWaitDelay):
		// The command itself succeeded, so this is not a failure — but the output
		// stops early, and the model should know why rather than guess.
		note = "The command exited, but something it started still held its output open, so rasp stopped reading there."
	}

	result.Content = bashContent(out.String(), note)
	return result, nil
}

func bashTimeout(ms int) time.Duration {
	switch {
	case ms <= 0:
		return bashDefaultTimeout
	// Compared as milliseconds rather than converted first: a value large enough
	// to overflow a Duration wraps to a negative one, and a deadline already in
	// the past kills the command before it runs.
	case ms > int(bashMaxTimeout/time.Millisecond):
		return bashMaxTimeout
	}
	return time.Duration(ms) * time.Millisecond
}

func bashContent(output, note string) string {
	switch {
	case note == "" && output == "":
		// "It printed nothing" is an observation; an empty content block is not.
		return "(no output)"
	case note == "":
		return output
	case output == "":
		return note
	}
	return strings.TrimRight(output, "\n") + "\n\n" + note
}

func bashTitle(command string) string {
	first, rest, _ := strings.Cut(command, "\n")
	if strings.TrimSpace(rest) != "" {
		first += " …"
	}
	return first
}

// lockedBuffer is the single writer both streams share. The lock is not for the
// two of them — os/exec gives one shared writer one copying goroutine — but for
// WaitDelay: Cmd.Stdout promises only that Wait completes when that goroutine
// reaches EOF, errors, "or a nonzero WaitDelay expires", so reading the buffer
// afterwards is entitled to race a copy still in flight. Go 1.26 awaits the
// goroutine anyway; the documented contract is the weaker one.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
