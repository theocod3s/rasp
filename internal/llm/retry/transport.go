package retry

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrDelayTooLong ends a request whose server asked to be retried further out
// than the transport tier will sleep.
var ErrDelayTooLong = errors.New("the provider asked for a longer wait than a turn will sleep through")

// Transport is the transport tier: an http.RoundTripper that repeats a request
// the server refused in a way repeating can fix. An adapter installs it on the
// HTTP client it hands its SDK, having set that SDK's own retry count to zero
// (decisions.md) — a vendor's timer ignores the turn's context and sleeps for
// whatever delay a provider names.
//
// The tier sits below streaming on purpose. A retried response is one whose
// status arrived and whose body nobody has read, so nothing above has seen an
// event yet; replaying a call that has already streamed half a reply is what
// design §3.1a rules out, and that failure belongs to the semantic tier.
type Transport struct {
	Base http.RoundTripper // http.DefaultTransport when nil

	// MaxAttempts counts the first call; DefaultMaxAttempts when zero.
	MaxAttempts int

	Backoff Backoff

	// MaxDelay is the ceiling on a wait the server asked for, past which the
	// request fails instead of sleeping; DefaultMaxDelay when zero. It does not
	// bound Backoff, which has a ceiling of its own.
	MaxDelay time.Duration

	// Sleep is how the wait is taken; an interruptible timer when nil.
	Sleep Sleeper

	// Now is what an HTTP-date Retry-After is measured against; time.Now when nil.
	Now func() time.Time
}

var _ http.RoundTripper = (*Transport)(nil)

// RoundTrip sends the request, and sends it again while the answer is a refusal
// another attempt can fix.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	for attempt := 1; ; attempt++ {
		resp, err := t.base().RoundTrip(req)
		if err != nil {
			// A connection that never produced a response is not retried here.
			// The bytes it consumed may have reached the provider, and design
			// §12 puts "connection reset" in the semantic tier's retryable
			// class, where a whole fresh call replaces a half-sent one.
			return nil, err
		}
		if !retryableStatus(resp.StatusCode) || attempt >= t.attempts() {
			return resp, nil
		}

		next := rewind(req)
		if next == nil {
			// A body that cannot be replayed makes the request unrepeatable, so
			// the refusal goes back as the server wrote it rather than becoming
			// an error of ours. An SDK marshalling JSON into a buffer always
			// gets a GetBody from http.NewRequest, so this is a door kept shut
			// rather than a path with traffic on it.
			return resp, nil
		}

		delay, named := t.delay(resp, attempt)
		drain(resp)
		if named && delay > t.maxDelay() {
			return nil, fmt.Errorf("%w: %s named %s and the ceiling is %s",
				ErrDelayTooLong, req.URL.Host, delay, t.maxDelay())
		}

		if err := t.sleep()(ctx, delay); err != nil {
			return nil, err
		}
		req = next
	}
}

// retryableStatus is design §12's transport list: request timeout, conflict,
// too many requests, and every 5xx.
func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests:
		return true
	}
	return status >= 500 && status <= 599
}

// delay is how long to wait before the next attempt, and whether the server
// named it rather than us computing it.
func (t *Transport) delay(resp *http.Response, attempt int) (time.Duration, bool) {
	if d, ok := retryAfter(resp.Header, t.now()); ok {
		return d, true
	}
	return t.Backoff.Delay(attempt), false
}

// retryAfter reads the wait a server asked for. Retry-After-Ms wins where both
// are present: a provider that sends it is answering the same question with the
// precision the second-granular header cannot carry.
//
// Retry-After is delta-seconds or an HTTP-date, and http.ParseTime covers all
// three date formats RFC 9110 still allows. A header this cannot parse is no
// header at all — a malformed one is not worth failing a request over, and the
// computed backoff is a safe answer to the same question.
func retryAfter(header http.Header, now time.Time) (time.Duration, bool) {
	if v := strings.TrimSpace(header.Get("Retry-After-Ms")); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil {
			if d, ok := scale(ms, time.Millisecond); ok {
				return d, true
			}
		}
	}

	v := strings.TrimSpace(header.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if d, ok := scale(secs, time.Second); ok {
			return d, true
		}
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(0, at.Sub(now)), true
	}
	return 0, false
}

// headerCeiling clamps a parsed header before it becomes a Duration. Converting
// a float too large for an int64 is undefined in Go, and a 1e30 that landed as a
// negative delay would read as "retry immediately" — the opposite of what the
// server asked for. Anything this far out is refused by MaxDelay anyway.
const headerCeiling = 24 * time.Hour

// scale turns a parsed header into a wait, reporting false for a number that is
// no answer at all.
func scale(v float64, unit time.Duration) (time.Duration, bool) {
	switch {
	case math.IsNaN(v):
		return 0, false
	case v <= 0:
		// A delay already in the past, which an HTTP-date Retry-After says the
		// same way: no wait left, rather than no header.
		return 0, true
	case v > float64(headerCeiling/unit):
		return headerCeiling, true
	}
	return time.Duration(v * float64(unit)), true
}

// rewind returns req with a fresh body for another attempt, or nil when the body
// cannot be replayed.
func rewind(req *http.Request) *http.Request {
	next := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return next
	}
	if req.GetBody == nil {
		return nil
	}
	body, err := req.GetBody()
	if err != nil {
		return nil
	}
	next.Body = body
	return next
}

const drainLimit = 64 << 10

// drain reads a little of a response nobody will look at and closes it, so the
// connection returns to the pool instead of being torn down before every retry.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, drainLimit))
	_ = resp.Body.Close()
}

func (t *Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

func (t *Transport) attempts() int {
	if t.MaxAttempts > 0 {
		return t.MaxAttempts
	}
	return DefaultMaxAttempts
}

func (t *Transport) maxDelay() time.Duration {
	if t.MaxDelay > 0 {
		return t.MaxDelay
	}
	return DefaultMaxDelay
}

func (t *Transport) sleep() Sleeper {
	if t.Sleep != nil {
		return t.Sleep
	}
	return wait
}

func (t *Transport) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}
