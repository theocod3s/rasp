package retry_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/retry"
)

// quota429 is the failure this tier exists for: an out-of-credit account,
// answered with the same status and the same words as a rate limit.
const quota429 = `POST "https://api.openai.com/v1/chat/completions": 429 Too Many Requests ` +
	`{"error":{"message":"You exceeded your current quota, please check your plan and billing details.",` +
	`"type":"insufficient_quota","code":"insufficient_quota"}}`

const rateLimit429 = `POST "https://api.anthropic.com/v1/messages": 429 Too Many Requests ` +
	`{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has ` +
	`exceeded your per-minute rate limit"}}`

func failed(err error) (*llm.Message, error) {
	return &llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopError}, err
}

// TestA429ForQuotaDoesNotSpendTheRetryBudget is design §12's reason for checking
// the quota list first. The budget is asserted twice: once that the quota
// failure left it whole, and once that a genuine rate limit afterwards still has
// every attempt it was supposed to have.
func TestA429ForQuotaDoesNotSpendTheRetryBudget(t *testing.T) {
	// The same failure with its quota words taken out has to come back
	// retryable, or nothing below proves an ordering: a classifier that read
	// this text as fatal for some other reason would pass just as well.
	bare := strings.NewReplacer("quota", "", "billing", "", "credit", "").Replace(quota429)
	if verdict := retry.Classify(failed(errors.New(bare))); verdict.Class != retry.ClassRetryable {
		t.Fatalf("stripped of its quota words the same 429 classifies as %q, so this test cannot "+
			"tell the quota list being consulted first from it being consulted at all", verdict.Class)
	}

	clock := &recorder{}
	budget := &retry.Budget{Sleep: clock.sleep}

	msg, err := failed(fmt.Errorf("openai: %w", errors.New(quota429)))
	again, verdict, waitErr := budget.Next(t.Context(), msg, err)
	switch {
	case again:
		t.Fatal("a call that failed for want of credit was sent again")
	case waitErr != nil:
		t.Fatalf("Next: %v", waitErr)
	case verdict.Class != retry.ClassFatal:
		t.Errorf("classified as %q, want %q", verdict.Class, retry.ClassFatal)
	case !strings.Contains(verdict.Hint, "credit"):
		t.Errorf("the hint %q says nothing about credit, which is the one thing that fixes it", verdict.Hint)
	}
	if budget.Retries() != 0 {
		t.Errorf("the budget spent %d retries on a failure it refused to retry", budget.Retries())
	}
	if len(clock.waited) != 0 {
		t.Errorf("waited %v before failing immediately", clock.waited)
	}

	limited, limitErr := failed(errors.New(rateLimit429))
	for attempt := 1; attempt <= retry.DefaultMaxAttempts-1; attempt++ {
		again, verdict, waitErr := budget.Next(t.Context(), limited, limitErr)
		if waitErr != nil {
			t.Fatalf("Next: %v", waitErr)
		}
		if !again {
			t.Fatalf("retry %d of %d was refused; the quota failure took the budget with it",
				attempt, retry.DefaultMaxAttempts-1)
		}
		if verdict.Class != retry.ClassRetryable {
			t.Fatalf("a rate limit classified as %q, want %q", verdict.Class, retry.ClassRetryable)
		}
	}
	if again, _, _ := budget.Next(t.Context(), limited, limitErr); again {
		t.Errorf("the budget allowed a %dth call, past MaxAttempts", retry.DefaultMaxAttempts+1)
	}
	if budget.Retries() != retry.DefaultMaxAttempts-1 {
		t.Errorf("the budget spent %d retries, want %d", budget.Retries(), retry.DefaultMaxAttempts-1)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  string
		want retry.Class
		hint string
	}{
		{"credit balance", `{"type":"invalid_request_error","message":"Your credit balance is too low ` +
			`to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}`,
			retry.ClassFatal, "credit"},
		{"quota as a 429", quota429, retry.ClassFatal, "credit"},
		{"rate limit", rateLimit429, retry.ClassRetryable, ""},
		{"overloaded", `529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			retry.ClassRetryable, ""},
		{"a 500", `500 Internal Server Error {"type":"error","error":{"type":"api_error",` +
			`"message":"Internal server error"}}`, retry.ClassRetryable, ""},
		{"a 503", `503 Service Unavailable`, retry.ClassRetryable, ""},
		{"a connection reset", "read tcp 10.0.0.2:443: connection reset by peer", retry.ClassRetryable, ""},
		{"a deadline", "Post \"https://api.anthropic.com\": context deadline exceeded", retry.ClassRetryable, ""},
		{"a stream cut short", "anthropic: stream ended without a stop reason", retry.ClassRetryable, ""},
		{"a rejected key", `401 {"type":"error","error":{"type":"authentication_error",` +
			`"message":"invalid x-api-key"}}`, retry.ClassFatal, "credential"},
		{"a model nobody has", `404 {"type":"error","error":{"type":"not_found_error",` +
			`"message":"model: claude-nope-20260101"}}`, retry.ClassFatal, "model id"},
		{"a prompt past the window", `400 {"type":"invalid_request_error","message":"prompt is too long: ` +
			`250000 tokens > 200000 maximum"}`, retry.ClassFatal, "context window"},
		{"the adapter's own context-window failure", "anthropic: the conversation is longer than the " +
			"model's context window; it has to be compacted or started again", retry.ClassFatal, "context window"},
		{"a failure nobody here has seen", `anthropic: unsupported content block "citation"`, retry.ClassFail, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			verdict := retry.Classify(failed(errors.New(tc.err)))
			if verdict.Class != tc.want {
				t.Fatalf("classified as %q, want %q", verdict.Class, tc.want)
			}
			if tc.hint == "" {
				if verdict.Hint != "" {
					t.Errorf("carries the hint %q, and only a fatal failure has one to give", verdict.Hint)
				}
				return
			}
			if !strings.Contains(verdict.Hint, tc.hint) {
				t.Errorf("the hint %q does not mention %q", verdict.Hint, tc.hint)
			}
		})
	}
}

// TestNothingToRetry covers the calls that are not failures. A cancelled turn is
// the one that would hurt: retrying it restarts work the user just stopped.
func TestNothingToRetry(t *testing.T) {
	tests := []struct {
		name string
		msg  *llm.Message
		err  error
	}{
		{"a finished answer", &llm.Message{StopReason: llm.StopEndTurn}, nil},
		{"tool calls to run", &llm.Message{StopReason: llm.StopToolUse}, nil},
		{"a reply cut off at the output limit", &llm.Message{StopReason: llm.StopMaxTokens}, nil},
		{"a refusal", &llm.Message{StopReason: llm.StopRefusal}, nil},
		// The stop reason alone, with an error that reads retryable: the adapter
		// has already decided this was an interrupt, and that decision is the one
		// that counts.
		{"an interrupted turn", &llm.Message{StopReason: llm.StopAborted},
			errors.New("anthropic: stream ended without a stop reason")},
		{"a cancellation reported as an error", &llm.Message{StopReason: llm.StopError},
			fmt.Errorf("anthropic: %w", context.Canceled)},
		{"no message at all", nil, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if verdict := retry.Classify(tc.msg, tc.err); verdict.Class != retry.ClassNone {
				t.Errorf("classified as %q, want %q", verdict.Class, retry.ClassNone)
			}

			clock := &recorder{}
			budget := &retry.Budget{Sleep: clock.sleep}
			again, _, err := budget.Next(t.Context(), tc.msg, tc.err)
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if again {
				t.Error("sent the call again")
			}
			if budget.Retries() != 0 {
				t.Errorf("spent %d retries", budget.Retries())
			}
		})
	}
}

// TestATimeoutIsNotACancellation holds the line the adapter's errorEvent draws
// the other side of: an expired deadline is one of §12's retryable failures, and
// classifying it as an interrupted turn would take it out of the retry path.
func TestATimeoutIsNotACancellation(t *testing.T) {
	msg, err := failed(fmt.Errorf("anthropic: %w", context.DeadlineExceeded))
	if verdict := retry.Classify(msg, err); verdict.Class != retry.ClassRetryable {
		t.Errorf("classified as %q, want %q", verdict.Class, retry.ClassRetryable)
	}
}

// TestARefusedWaitIsFatalThroughEveryWrapper follows the transport tier's own
// error out to where the semantic tier sees it: wrapped by net/http, then by the
// adapter. Retrying it would sleep out the same refusal three times.
func TestARefusedWaitIsFatalThroughEveryWrapper(t *testing.T) {
	transportErr := fmt.Errorf("%w: it asked to be retried in 10m0s and the ceiling is 1m0s", retry.ErrDelayTooLong)
	wrapped := fmt.Errorf("anthropic: %w", &url.Error{
		Op:  "Post",
		URL: "https://api.anthropic.com/v1/messages",
		Err: transportErr,
	})

	verdict := retry.Classify(failed(wrapped))
	if verdict.Class != retry.ClassFatal {
		t.Fatalf("classified as %q, want %q", verdict.Class, retry.ClassFatal)
	}
	if verdict.Hint == "" {
		t.Error("a fatal verdict with no hint leaves the user nothing to act on")
	}
}

func TestTheSemanticBackoffClimbsAndIsInterruptible(t *testing.T) {
	msg, err := failed(errors.New(rateLimit429))

	clock := &recorder{}
	budget := &retry.Budget{MaxAttempts: 4, Sleep: clock.sleep, Backoff: retry.Backoff{Base: time.Second, Rand: fixed(0)}}
	for range 3 {
		if again, _, err := budget.Next(t.Context(), msg, err); !again || err != nil {
			t.Fatalf("Next: again=%v err=%v", again, err)
		}
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if fmt.Sprint(clock.waited) != fmt.Sprint(want) {
		t.Errorf("waited %v, want %v", clock.waited, want)
	}

	// No Sleep injected: the real one, asked for an hour and interrupted.
	interruptible := &retry.Budget{Backoff: retry.Backoff{Base: time.Hour}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		again, _, err := interruptible.Next(ctx, msg, err)
		if again {
			done <- errors.New("an interrupted wait still asked for another call")
			return
		}
		done <- err
	}()
	cancel()

	select {
	case got := <-done:
		if !errors.Is(got, context.Canceled) {
			t.Fatalf("an interrupted wait failed with %v, want context.Canceled", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the wait outlived the context that was supposed to end it")
	}
}
