package builtin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
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

	// bashMaxOutput caps the whole of what a shell call puts in front of the
	// model — output, omission marker and closing note together. 50 KiB is around
	// 13k tokens: survivable for one turn, and the point past which `cat`-ing a
	// file is a mistake rather than an answer.
	bashMaxOutput = 50 << 10
)

const bashDescription = `Run a command with bash, from the workspace root — the same directory a relative path
in the file tools is resolved against.

Output is stdout and stderr together, in the order the command wrote them. A non-zero exit is
reported back rather than hidden: read the output and decide what it means. On a timeout or an
interrupted turn the command is killed along with everything it started, so anything meant to
outlive the call has to detach itself.

Long output comes back with its middle dropped, keeping both ends, and the whole of it is saved
to a file this tool names — narrow the command, or read that file, rather than running it again.

Prefer the dedicated tools for reading, searching and editing files — their output is structured
and far cheaper.`

// BashInput is one shell call as the model asks for it.
type BashInput struct {
	Command   string `json:"command"              desc:"The command to run, as bash would read it: pipes, redirections and && all mean what they look like."`
	TimeoutMS int    `json:"timeout_ms,omitempty" desc:"How long to let the command run, in milliseconds. Defaults to 120000 and is capped at 600000."`
}

// BashDetails is what the UI draws a shell call from; the model sees none of it.
type BashDetails struct {
	Command   string
	ExitCode  int
	Duration  time.Duration
	Truncated bool
	SpillPath string // the whole output, when Truncated and it could be saved
}

// NewBash returns the bash tool, running every command in dir — the workspace
// root, which is what the file tools resolve a relative path against. Left to
// the process's own working directory, `read config.go` and `cat config.go` name
// different files, and the shell command a model writes to check the edit it
// just made inspects something else entirely.
//
// It does not implement Sequential: built-in tools are parallel because we
// audited them (design §6 rule 4), and the commands that warrant serializing are
// the ones the permission gate stops on anyway, which is already a serial
// barrier (§6 rule 5). Declaring it here instead would degrade every batch
// holding a shell call — `go test` beside three reads — to serial.
func NewBash(dir string) tool.Tool {
	if dir == "" {
		panic("builtin: bash needs the directory to run commands in, and the workspace root is it; " +
			"an empty one leaves every relative path meaning whatever rasp's own working directory makes it")
	}
	return tool.New("bash", bashDescription, func(ctx context.Context, in BashInput) (tool.Result, error) {
		return runBash(ctx, dir, in)
	})
}

func runBash(ctx context.Context, dir string, in BashInput) (tool.Result, error) {
	if strings.TrimSpace(in.Command) == "" {
		return tool.Result{IsError: true, Content: "bash was called with no command to run."}, nil
	}

	timeout := bashTimeout(in.TimeoutMS)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := &lockedBuffer{}
	cmd := exec.CommandContext(runCtx, "bash", "-c", in.Command)
	cmd.Dir = dir
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
		// Not a failure — the command exited 0 — but its output stops early and the
		// model should be told rather than left to infer. Only the clean path can
		// be told: Wait prefers the exit status over the pipe's error, so a command
		// that both fails and leaves something holding its output reports the
		// status and nothing about the truncation. Reordering this switch does not
		// recover it; the error never arrives.
		note = "The command exited, but something it started still held its output open, so rasp stopped reading there."
	}

	result.Content, details.Truncated, details.SpillPath = bashOutput(out.String(), note)
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

// bashOutput is everything the model is shown, bounded to bashMaxOutput bytes
// with the note counted in — so nothing appended after the output can push the
// result past the cap.
func bashOutput(output, note string) (content string, truncated bool, spill string) {
	if len(output)+noteCost(note) <= bashMaxOutput {
		return bashContent(output, note), false, ""
	}

	spill, err := spillOutput(output)
	if err != nil {
		note = joinNotes(note, fmt.Sprintf("The output was too long to return whole, and rasp could not save the rest: %v", err))
		spill = ""
	} else {
		note = joinNotes(note, fmt.Sprintf("The output was too long to return whole. All %d bytes of it are in %s, which a shell command can read.", len(output), spill))
	}
	return bashContent(headAndTail(output, bashMaxOutput-noteCost(note), bashOmitted), note), true, spill
}

// headAndTail bounds s to limit bytes by dropping its middle, marker(dropped)
// standing in for what went. Both ends survive because the head carries the
// command echo and the tail the error; keeping either alone loses the diagnosis
// (internals §3.5).
//
// The marker's own length depends on the count it reports, which depends on how
// much room the marker left — and sizing it for the largest count it could ever
// carry, every byte of s, breaks that circle in the safe direction. Measuring
// the marker after choosing the split instead is the off-by-one that returns
// limit+1 bytes.
func headAndTail(s string, limit int, marker func(dropped int) string) string {
	switch {
	case limit <= 0:
		return ""
	case len(s) <= limit:
		return s
	}

	keep := limit - len(marker(len(s)))
	if keep <= 0 {
		// The marker alone does not fit, and the cap outranks saying anything.
		return s[:limit]
	}

	head, tail := s[:keep/2], s[len(s)-(keep-keep/2):]
	return head + marker(len(s)-len(head)-len(tail)) + tail
}

func bashOmitted(dropped int) string {
	return fmt.Sprintf("\n\n[%d bytes omitted from the middle of the output]\n\n", dropped)
}

// spillOutput writes the whole of an output too long to return to the OS temp
// directory, at the 0600 os.CreateTemp gives it — a command prints whatever it
// prints, secrets included.
//
// Nothing deletes it: no part of rasp owns temp-file lifetime yet, and the temp
// directory is the one place a file is reaped without an owner. A write that
// fails part-way leaves its file behind for the same reason — removing it would
// put an os.Remove in a package whose filesystem access is confined to
// workspace, and that exemption is worth more than the stray file.
func spillOutput(output string) (string, error) {
	f, err := os.CreateTemp("", "rasp-bash-*.log")
	if err != nil {
		return "", err
	}

	_, err = f.WriteString(output)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	return f.Name(), nil
}

func joinNotes(a, b string) string {
	if a == "" {
		return b
	}
	return a + " " + b
}

// noteCost is what appending note to the output costs, separator included. It
// rounds up where bashContent trims, so a budget built on it is never short.
func noteCost(note string) int {
	if note == "" {
		return 0
	}
	return len(note) + 2
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
