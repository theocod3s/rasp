package retry

import (
	"context"
	"errors"
	"strings"

	"github.com/theocod3s/rasp/internal/llm"
)

// Class is what the semantic tier made of a finished model call.
type Class string

const (
	// ClassNone is a call that did not fail, and a turn the user cancelled.
	// Neither is anything to retry or to report.
	ClassNone Class = ""

	// ClassRetryable is a failure the same call may not hit again.
	ClassRetryable Class = "retryable"

	// ClassFatal is a failure that repeats until someone changes something, and
	// Verdict.Hint says what.
	ClassFatal Class = "fatal"

	// ClassFail is a failure this package does not recognise. Deliberately not
	// retried: repeating an unknown failure spends a budget that exists for the
	// failures we can name, and the provider's own words are what the user needs.
	ClassFail Class = "fail"
)

// Verdict is one classification.
type Verdict struct {
	Class Class

	// Hint is what someone can do about it, and is empty unless Class is
	// ClassFatal.
	Hint string
}

// Classify decides what a finished model call earned: the message the stream
// accumulated, and the error its terminal event carried.
//
// The error is a second argument because a Message has nowhere to keep one: the
// stop reason says a call failed, and the text saying WHY — which every rule
// below matches on — exists only on the terminal event (design §3.1). Pure
// either way, which is the property design §12 is after.
func Classify(msg *llm.Message, err error) Verdict {
	var stop llm.StopReason
	if msg != nil {
		stop = msg.StopReason
	}

	switch {
	case stop == llm.StopAborted, errors.Is(err, context.Canceled):
		// A cancelled turn is a completion, not a failure (design §4's
		// termination table), and retrying one would restart work the user just
		// stopped. context.DeadlineExceeded is deliberately not here: §12 counts
		// a timeout among the failures worth another call.
		return Verdict{Class: ClassNone}

	case err == nil && stop != llm.StopError:
		return Verdict{Class: ClassNone}

	case errors.Is(err, ErrDelayTooLong):
		return Verdict{Class: ClassFatal, Hint: hintRateLimited}
	}

	text := normalize(err)
	for _, p := range patterns {
		for _, term := range p.terms {
			if strings.Contains(text, term) {
				return Verdict{Class: p.class, Hint: p.hint}
			}
		}
	}
	return Verdict{Class: ClassFail}
}

// normalize lowercases the failure text and spells `_` as a space. Providers
// name one condition both ways in one payload — `"type": "rate_limit_error"`
// beside prose about a rate limit — so every term below is written with spaces
// and matches either.
func normalize(err error) string {
	if err == nil {
		return ""
	}
	return strings.ReplaceAll(strings.ToLower(err.Error()), "_", " ")
}

const (
	hintQuota = "the account has no credit left or has hit a spend limit; retrying cannot fix it, " +
		"so top up or raise the limit with the provider and start the turn again"
	hintCredential = "the credential was rejected; check the key rasp is configured with, and the " +
		"environment variable it expands from, against the provider's console"
	hintModel = "the provider has no such model; check the model id in the config, and the base URL " +
		"if this is a gateway, since a wrong endpoint answers the same way"
	hintContext = "the conversation no longer fits the model's context window; compact it or start a " +
		"new session"
	hintRateLimited = "the provider is rate limiting for longer than a turn should wait; try again " +
		"later, or with a different model"
)

// patterns is design §12's table in the order it is applied, and the order is
// the load-bearing part.
//
// Quota and billing come FIRST: they arrive as a 429 whose body also reads as a
// rate limit, so any later rule would call one retryable and spend the budget on
// requests that cannot succeed. Retryable comes LAST for the same reason in the
// small — its terms are the generic ones ("timeout", "overloaded"), and a
// specific failure that happens to mention one is still specific.
var patterns = []struct {
	class Class
	hint  string
	terms []string
}{
	{ClassFatal, hintQuota, []string{
		"insufficient quota", "quota exceeded", "billing", "credit balance",
	}},
	{ClassFatal, hintCredential, []string{
		"invalid api key", "invalid x-api-key", "authentication error", "permission error", "unauthorized",
	}},
	{ClassFatal, hintModel, []string{
		"model not found", "unknown model", "not found error",
	}},
	{ClassFatal, hintContext, []string{
		"context length exceeded", "context window", "prompt is too long", "request too large",
	}},
	{ClassRetryable, "", []string{
		"overloaded", "rate limit", "too many requests",
		"internal server error", "bad gateway", "service unavailable", "api error",
		"timeout", "timed out", "deadline exceeded",
		"connection reset", "connection refused", "unexpected eof", "stream ended",
	}},
}

// Budget is one model call's allowance under the semantic tier: how many more
// times the loop may make that call, and the wait before each. One turn owns
// one, and nothing else touches it.
type Budget struct {
	// MaxAttempts counts the first call; DefaultMaxAttempts when zero.
	MaxAttempts int

	Backoff Backoff

	// Sleep is how the wait is taken; an interruptible timer when nil.
	Sleep Sleeper

	retries int
}

// Next classifies the call that produced msg and err, waits out this attempt's
// backoff when the verdict is retryable and budget remains, and reports whether
// to make that call again. A non-nil error is the context ending during the
// wait; the verdict comes back either way, so a caller that is not calling again
// still has the class and the hint to report.
//
// Only a retryable verdict spends anything, which is the point of classifying
// before counting: a 429 that means "out of credit" leaves the budget whole.
func (b *Budget) Next(ctx context.Context, msg *llm.Message, err error) (bool, Verdict, error) {
	verdict := Classify(msg, err)
	if verdict.Class != ClassRetryable {
		return false, verdict, nil
	}

	attempts := b.MaxAttempts
	if attempts <= 0 {
		attempts = DefaultMaxAttempts
	}
	if b.retries >= attempts-1 {
		return false, verdict, nil
	}
	b.retries++

	sleep := b.Sleep
	if sleep == nil {
		sleep = wait
	}
	if err := sleep(ctx, b.Backoff.Delay(b.retries)); err != nil {
		return false, verdict, err
	}
	return true, verdict, nil
}

// Retries is how many retries this budget has spent.
func (b *Budget) Retries() int { return b.retries }
