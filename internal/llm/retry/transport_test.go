package retry_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/llm/retry"
)

// fixed is a Rand that draws the whole jitter, so a computed delay is a number a
// test can name: 25% off whatever the ladder produced.
func fixed(f float64) func() float64 { return func() float64 { return f } }

func TestRetriesWhatDesignSaysAndNothingElse(t *testing.T) {
	tests := []struct {
		status int
		want   int
	}{
		{http.StatusRequestTimeout, 2},
		{http.StatusConflict, 2},
		{http.StatusTooManyRequests, 2},
		{http.StatusInternalServerError, 2},
		{http.StatusBadGateway, 2},
		{http.StatusServiceUnavailable, 2},
		{529, 2}, // Anthropic's overloaded
		{http.StatusOK, 1},
		{http.StatusBadRequest, 1},
		{http.StatusUnauthorized, 1},
		{http.StatusForbidden, 1},
		{http.StatusNotFound, 1},
		{http.StatusUnprocessableEntity, 1},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%d", tc.status), func(t *testing.T) {
			far := &server{replies: []reply{status(tc.status), status(http.StatusOK)}}
			clock := &recorder{}
			transport := &retry.Transport{Base: far, MaxAttempts: 2, Sleep: clock.sleep}

			resp, err := transport.RoundTrip(request(t, t.Context(), "{}"))
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			defer resp.Body.Close()

			if far.calls() != tc.want {
				t.Errorf("the far side saw %d attempt(s), want %d", far.calls(), tc.want)
			}
			if len(clock.waited) != tc.want-1 {
				t.Errorf("waited %d time(s) for %d attempt(s)", len(clock.waited), tc.want)
			}
		})
	}
}

func TestARetriedRequestIsSentAgainWholeAndTheRefusalIsClosed(t *testing.T) {
	far := &server{replies: []reply{status(http.StatusTooManyRequests), status(http.StatusOK)}}
	clock := &recorder{}
	transport := &retry.Transport{Base: far, Sleep: clock.sleep}

	resp, err := transport.RoundTrip(request(t, t.Context(), `{"model":"claude"}`))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	want := []string{`{"model":"claude"}`, `{"model":"claude"}`}
	if len(far.bodies) != 2 || far.bodies[0] != want[0] || far.bodies[1] != want[1] {
		t.Errorf("the far side read %q, want %q", far.bodies, want)
	}
	if !far.sent[0].closed {
		t.Error("the refused response was never closed, so its connection cannot go back to the pool")
	}
}

func TestTheServerNamesTheWait(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		reply reply
		want  time.Duration
	}{
		{"seconds", status(429).with("Retry-After", "2"), 2 * time.Second},
		{"fractional seconds", status(429).with("Retry-After", "1.5"), 1500 * time.Millisecond},
		{"milliseconds", status(429).with("Retry-After-Ms", "250"), 250 * time.Millisecond},
		{
			// Both headers, which is what an OpenAI-compatible gateway sends.
			"milliseconds win",
			status(429).with("Retry-After", "30").with("Retry-After-Ms", "400"),
			400 * time.Millisecond,
		},
		{"http date", status(429).with("Retry-After", now.Add(5*time.Second).Format(http.TimeFormat)), 5 * time.Second},
		{"a date already past", status(429).with("Retry-After", now.Add(-time.Hour).Format(http.TimeFormat)), 0},
		{"a header nobody can parse", status(429).with("Retry-After", "soon"), 750 * time.Millisecond},
		{"no header at all", status(429), 750 * time.Millisecond},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			far := &server{replies: []reply{tc.reply, status(http.StatusOK)}}
			clock := &recorder{}
			transport := &retry.Transport{
				Base:    far,
				Sleep:   clock.sleep,
				Now:     func() time.Time { return now },
				Backoff: retry.Backoff{Base: time.Second, Rand: fixed(1)},
			}

			resp, err := transport.RoundTrip(request(t, t.Context(), "{}"))
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			defer resp.Body.Close()

			if len(clock.waited) != 1 {
				t.Fatalf("waited %d times, want one wait between two attempts", len(clock.waited))
			}
			if clock.waited[0] != tc.want {
				t.Errorf("waited %s, want %s", clock.waited[0], tc.want)
			}
		})
	}
}

// TestTheComputedBackoffClimbsAndJitters pins the ladder itself: the header is
// absent, so every wait is ours.
func TestTheComputedBackoffClimbsAndJitters(t *testing.T) {
	far := &server{replies: []reply{status(429), status(429), status(429), status(http.StatusOK)}}
	clock := &recorder{}
	transport := &retry.Transport{
		Base:        far,
		MaxAttempts: 4,
		Sleep:       clock.sleep,
		Backoff:     retry.Backoff{Base: time.Second, Rand: fixed(0)},
	}

	resp, err := transport.RoundTrip(request(t, t.Context(), "{}"))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if len(clock.waited) != len(want) {
		t.Fatalf("waited %v, want %v", clock.waited, want)
	}
	for i, d := range want {
		if clock.waited[i] != d {
			t.Errorf("wait %d was %s, want %s", i+1, clock.waited[i], d)
		}
	}
}

func TestJitterOnlyEverSubtracts(t *testing.T) {
	full := retry.Backoff{Base: time.Second, Rand: fixed(1)}.Delay(1)
	none := retry.Backoff{Base: time.Second, Rand: fixed(0)}.Delay(1)

	if none != time.Second {
		t.Errorf("an undrawn jitter gave %s, want the base %s", none, time.Second)
	}
	if full != 750*time.Millisecond {
		t.Errorf("a fully drawn jitter gave %s, want a quarter off at %s", full, 750*time.Millisecond)
	}
}

func TestTheBackoffStopsClimbingAtItsCap(t *testing.T) {
	// A base the cap is not a multiple of, so the last doubling overshoots and
	// something has to clamp it. A power-of-two base lands on the cap exactly and
	// would pass with no clamp at all.
	backoff := retry.Backoff{Base: 3 * time.Second, Cap: 8 * time.Second, Rand: fixed(0)}
	for _, n := range []int{3, 20, 1 << 20} {
		if d := backoff.Delay(n); d != 8*time.Second {
			t.Errorf("retry %d would wait %s, want the cap %s", n, d, 8*time.Second)
		}
	}
}

// TestAWaitPastTheCapThrowsRatherThanSleeping is design §12's sharp detail: a
// provider asking for ten minutes should surface as a failure, not as a turn
// that hangs for ten minutes.
func TestAWaitPastTheCapThrowsRatherThanSleeping(t *testing.T) {
	far := &server{replies: []reply{status(429).with("Retry-After", "600"), status(http.StatusOK)}}
	clock := &recorder{}
	transport := &retry.Transport{Base: far, Sleep: clock.sleep, MaxDelay: time.Minute}

	resp, err := transport.RoundTrip(request(t, t.Context(), "{}"))
	if resp != nil {
		resp.Body.Close()
		t.Fatal("a refused wait returned a response as well as an error")
	}
	if !errors.Is(err, retry.ErrDelayTooLong) {
		t.Fatalf("RoundTrip failed with %v, want it to wrap ErrDelayTooLong", err)
	}
	if !strings.Contains(err.Error(), "10m0s") || !strings.Contains(err.Error(), "1m0s") {
		t.Errorf("the error %q names neither the wait asked for nor the ceiling", err)
	}
	if len(clock.waited) != 0 {
		t.Errorf("slept %v on the way to refusing to sleep", clock.waited)
	}
	if far.calls() != 1 {
		t.Errorf("the far side saw %d attempts, want the one it refused", far.calls())
	}
	if !far.sent[0].closed {
		t.Error("the refused response was never closed")
	}
}

func TestTheCapIsAnUpperBoundNotALimit(t *testing.T) {
	for _, tc := range []struct {
		asked string
		throw bool
	}{
		{"60", false},
		{"60.001", true},
	} {
		t.Run(tc.asked, func(t *testing.T) {
			far := &server{replies: []reply{status(429).with("Retry-After", tc.asked), status(http.StatusOK)}}
			clock := &recorder{}
			transport := &retry.Transport{Base: far, Sleep: clock.sleep, MaxDelay: time.Minute}

			resp, err := transport.RoundTrip(request(t, t.Context(), "{}"))
			if resp != nil {
				defer resp.Body.Close()
			}
			if throw := errors.Is(err, retry.ErrDelayTooLong); throw != tc.throw {
				t.Fatalf("a wait of %ss refused=%v, want %v (err %v)", tc.asked, throw, tc.throw, err)
			}
		})
	}
}

// TestAnAbsurdRetryAfterIsClampedBeforeItBecomesADuration guards the float
// conversion: a value too large for an int64 is undefined in Go, and a wrapped
// negative Duration would mean "retry now" — the opposite of what the server
// asked for. The ceiling here is a fortnight, so the clamp is the only thing
// deciding the wait, and a day is what it has to decide.
func TestAnAbsurdRetryAfterIsClampedBeforeItBecomesADuration(t *testing.T) {
	for _, asked := range []string{"1e30", "99999999999999999999", "Inf"} {
		t.Run(asked, func(t *testing.T) {
			far := &server{replies: []reply{status(429).with("Retry-After", asked), status(http.StatusOK)}}
			clock := &recorder{}
			transport := &retry.Transport{Base: far, Sleep: clock.sleep, MaxDelay: 14 * 24 * time.Hour}

			resp, err := transport.RoundTrip(request(t, t.Context(), "{}"))
			if resp != nil {
				defer resp.Body.Close()
			}
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			if len(clock.waited) != 1 || clock.waited[0] != 24*time.Hour {
				t.Fatalf("a %q Retry-After waited %v, want one wait of a day", asked, clock.waited)
			}
		})
	}
}

// TestARetryAfterNobodyCanUseFallsBackToTheComputedWait is the other half: a
// number that is no answer at all leaves the backoff to answer instead.
func TestARetryAfterNobodyCanUseFallsBackToTheComputedWait(t *testing.T) {
	for _, asked := range []string{"NaN", "soon", ""} {
		t.Run(asked, func(t *testing.T) {
			far := &server{replies: []reply{status(429).with("Retry-After", asked), status(http.StatusOK)}}
			clock := &recorder{}
			transport := &retry.Transport{
				Base:    far,
				Sleep:   clock.sleep,
				Backoff: retry.Backoff{Base: 2 * time.Second, Rand: fixed(0)},
			}

			resp, err := transport.RoundTrip(request(t, t.Context(), "{}"))
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			defer resp.Body.Close()

			if len(clock.waited) != 1 || clock.waited[0] != 2*time.Second {
				t.Fatalf("a %q Retry-After waited %v, want the computed 2s", asked, clock.waited)
			}
		})
	}
}

func TestCancellingTheTurnInterruptsTheWait(t *testing.T) {
	// No Sleep injected: this is the only test of the real one, and the wait it
	// interrupts is thirty seconds long, so a pass cannot be a slow timer.
	answered := make(chan struct{})
	var once sync.Once
	far := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		// Answering every attempt, not only the first: a transport that ignored
		// the context would take this door, and the test should fail on its own
		// assertion rather than on a second answer nobody scripted.
		defer once.Do(func() { close(answered) })
		header := http.Header{}
		header.Set("Retry-After", "30")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})
	transport := &retry.Transport{Base: far}

	ctx, cancel := context.WithCancel(t.Context())
	req := request(t, ctx, "{}")

	type result struct {
		resp *http.Response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := transport.RoundTrip(req)
		done <- result{resp, err}
	}()

	<-answered // the refusal has landed, so the wait has begun
	cancel()

	select {
	case got := <-done:
		if got.resp != nil {
			got.resp.Body.Close()
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("an interrupted wait failed with %v, want context.Canceled", got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the wait outlived the context that was supposed to end it")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestGivingUpReturnsTheServersOwnRefusal(t *testing.T) {
	far := &server{replies: []reply{status(429), status(429), status(429)}}
	clock := &recorder{}
	transport := &retry.Transport{Base: far, Sleep: clock.sleep}

	resp, err := transport.RoundTrip(request(t, t.Context(), "{}"))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	if far.calls() != retry.DefaultMaxAttempts {
		t.Errorf("the far side saw %d attempts, want %d", far.calls(), retry.DefaultMaxAttempts)
	}
	if resp.StatusCode != 429 {
		t.Errorf("gave back %d; the last refusal is what the semantic tier classifies", resp.StatusCode)
	}
	if body, _ := io.ReadAll(resp.Body); len(body) == 0 {
		t.Error("the last response arrived with its body already drained")
	}
}

func TestAReplyThatNeverArrivedIsLeftToTheSemanticTier(t *testing.T) {
	broken := errors.New("read tcp: connection reset by peer")
	far := &server{replies: []reply{{err: broken}, status(http.StatusOK)}}
	clock := &recorder{}
	transport := &retry.Transport{Base: far, Sleep: clock.sleep}

	resp, err := transport.RoundTrip(request(t, t.Context(), "{}"))
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, broken) {
		t.Fatalf("RoundTrip returned %v, want the transport's own error", err)
	}
	if far.calls() != 1 {
		t.Errorf("the far side saw %d attempts; a request that may already have been "+
			"delivered is not replayed here", far.calls())
	}
}

func TestABodyThatCannotBeRewoundIsNotReplayed(t *testing.T) {
	far := &server{replies: []reply{status(429), status(http.StatusOK)}}
	clock := &recorder{}
	transport := &retry.Transport{Base: far, Sleep: clock.sleep}

	// A reader of a type http.NewRequest cannot rewind, so GetBody stays nil —
	// the shape this path exists for.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.example/v1/messages",
		io.NopCloser(strings.NewReader("{}")))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if req.GetBody != nil {
		t.Fatal("this request can rewind after all, so the test proves nothing")
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	if far.calls() != 1 {
		t.Errorf("the far side saw %d attempts, want the one that could be sent", far.calls())
	}
	if resp.StatusCode != 429 {
		t.Errorf("gave back %d, want the server's own refusal", resp.StatusCode)
	}
}
