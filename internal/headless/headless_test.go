package headless_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/headless"
	"github.com/theocod3s/rasp/internal/llm"
)

func TestRunPrintsTheReplyOnce(t *testing.T) {
	var out bytes.Buffer
	err := run(t, &out, text("Hel", "lo"), text(", world"), done(llm.StopEndTurn))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Exactly, not merely containing: Partial is the whole message every time, so
	// a runner printing it rather than the new tail writes "HelHello, world".
	if got := out.String(); got != "Hello, world" {
		t.Errorf("stdout = %q, want %q", got, "Hello, world")
	}
}

// TestRunWritesBeforeTheStreamEnds is the streaming half of the same criterion.
// The handoff writer is unbuffered, so the first receive can only succeed while
// the provider is still mid-script.
func TestRunWritesBeforeTheStreamEnds(t *testing.T) {
	writes := make(handoff)
	returned := make(chan error, 1)
	provider := &scripted{steps: []step{text("one", "two"), done(llm.StopEndTurn)}}

	go func() {
		returned <- headless.Runner{Provider: provider, Model: "m", Out: writes}.
			Run(context.Background(), "hi")
	}()

	if got := writes.take(t); got != "one" {
		t.Fatalf("first write = %q, want %q — output was held back past the first chunk", got, "one")
	}
	select {
	case err := <-returned:
		t.Fatalf("Run returned before its first chunk was taken: %v", err)
	default:
	}
	if got := writes.take(t); got != "two" {
		t.Fatalf("second write = %q, want %q", got, "two")
	}

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the stream finished")
	}
}

// TestOutputComesFromPartialNotDelta drives a provider whose deltas disagree
// with its message. Partial is the authority, so the reply survives it.
func TestOutputComesFromPartialNotDelta(t *testing.T) {
	var out bytes.Buffer
	err := run(t, &out, miscounted("ans", "wer"), done(llm.StopEndTurn))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); got != "answer" {
		t.Errorf("stdout = %q, want %q — deltas were reassembled instead of read off Partial", got, "answer")
	}
}

// TestThinkingStaysOutOfTheReply: stdout is a script's input, and reasoning is
// not part of the answer.
func TestThinkingStaysOutOfTheReply(t *testing.T) {
	var out bytes.Buffer
	err := run(t, &out, thinking("deliberating"), text("42"), done(llm.StopEndTurn))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); got != "42" {
		t.Errorf("stdout = %q, want %q", got, "42")
	}
}

func TestRunReturnsTheStreamsFailureAndKeepsWhatArrived(t *testing.T) {
	refused := errors.New("anthropic: 429 rate limited")

	var out bytes.Buffer
	err := run(t, &out, text("half an "), failed(refused, "answer"))
	if !errors.Is(err, refused) {
		t.Fatalf("Run returned %v, want %v", err, refused)
	}
	// Everything Partial held, including what the failing event was the first to
	// carry. Text is not lost because the event announcing it was bad news.
	if got := out.String(); got != "half an answer" {
		t.Errorf("stdout = %q, want everything that arrived before the failure", got)
	}
}

// TestRunFailsWhenTheStreamNeverTerminates guards the case a process cannot see
// for itself: a provider that stops yielding without saying whether it finished.
func TestRunFailsWhenTheStreamNeverTerminates(t *testing.T) {
	var out bytes.Buffer
	err := run(t, &out, text("half an "))
	if err == nil {
		t.Fatal("Run succeeded on a stream with no terminal event")
	}
	if !strings.Contains(err.Error(), "terminal event") {
		t.Errorf("error %q does not say the stream never terminated", err)
	}
}

func TestRunWarnsWhenTheReplyWasCutOff(t *testing.T) {
	var out, warn bytes.Buffer
	runner := headless.Runner{Provider: &scripted{steps: []step{
		text("as far as it g"), done(llm.StopMaxTokens),
	}}, Model: "m", Out: &out, Warn: &warn}

	// Truncation is a completed run, per design §4's termination table: the
	// warning is what stops it reading as a whole answer.
	if err := runner.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); got != "as far as it g" {
		t.Errorf("stdout = %q", got)
	}
	if !strings.Contains(warn.String(), "cut off") {
		t.Errorf("stderr = %q, want a warning that the reply was cut off", warn.String())
	}
}

func TestRunReportsAFailedWrite(t *testing.T) {
	broken := errors.New("broken pipe")
	runner := headless.Runner{
		Provider: &scripted{steps: []step{text("hello"), done(llm.StopEndTurn)}},
		Model:    "m",
		Out:      writerFunc(func([]byte) (int, error) { return 0, broken }),
	}
	if err := runner.Run(context.Background(), "hi"); !errors.Is(err, broken) {
		t.Fatalf("Run returned %v, want %v", err, broken)
	}
}

// TestRunAsksForOneUserMessage checks the request the runner builds, which is
// the half no output can show: an empty MaxTokens or a prompt that never made it
// into a block is a round trip spent being told so.
func TestRunAsksForOneUserMessage(t *testing.T) {
	provider := &scripted{steps: []step{text("ok"), done(llm.StopEndTurn)}}
	runner := headless.Runner{Provider: provider, Model: "claude-opus-5", Out: &bytes.Buffer{}}
	if err := runner.Run(context.Background(), "why is the sky blue?"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	req := provider.req
	if req.Model != "claude-opus-5" {
		t.Errorf("request model = %q", req.Model)
	}
	if req.MaxTokens != headless.DefaultMaxTokens {
		t.Errorf("request MaxTokens = %d, want %d", req.MaxTokens, headless.DefaultMaxTokens)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != llm.RoleUser {
		t.Fatalf("request messages = %+v, want one user message", req.Messages)
	}
	content := req.Messages[0].Content
	if len(content) != 1 || content[0].Type != llm.BlockText || content[0].Text != "why is the sky blue?" {
		t.Errorf("request content = %+v, want the prompt in one text block", content)
	}
}

// TestScriptedProviderMeetsTheStreamContract keeps the tests above resting on a
// provider a real one resembles. Without it they could all pass against a double
// no adapter is allowed to be.
func TestScriptedProviderMeetsTheStreamContract(t *testing.T) {
	provider := &scripted{steps: []step{
		thinking("first"), text("Hel", "lo"), text(", world"), done(llm.StopEndTurn),
	}}
	if _, err := llm.CheckStream(provider.Stream(context.Background(), llm.Request{Model: "m"})); err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
}

// run drives the runner to completion with a scripted provider.
func run(t *testing.T, out *bytes.Buffer, steps ...step) error {
	t.Helper()
	runner := headless.Runner{Provider: &scripted{steps: steps}, Model: "m", Out: out}
	return runner.Run(context.Background(), "hi")
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
