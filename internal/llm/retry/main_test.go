package retry_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// TestMain runs the leak detector over the package. Both tiers wait on a timer
// and a context, and a wait nothing releases is a turn that never ends.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// reply is one scripted answer from the far side: a status with headers, or a
// failure to reach it at all.
type reply struct {
	status int
	header http.Header
	err    error
}

func status(code int) reply { return reply{status: code} }

func (r reply) with(key, value string) reply {
	header := http.Header{}
	for k, v := range r.header {
		header[k] = v
	}
	header.Set(key, value)
	r.header = header
	return r
}

// server is a base RoundTripper that answers from a script and records what each
// attempt sent, so a test can assert on the request the retry replayed as well
// as on the count.
type server struct {
	replies []reply
	bodies  []string
	sent    []*body
}

func (s *server) RoundTrip(req *http.Request) (*http.Response, error) {
	sent := ""
	if req.Body != nil {
		read, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		sent = string(read)
	}
	s.bodies = append(s.bodies, sent)

	if len(s.bodies) > len(s.replies) {
		// Running past the script is the failure that would otherwise look like
		// a pass: a test scripting two replies proves nothing about the second
		// if the transport asked for a third.
		return nil, errUnscripted
	}
	r := s.replies[len(s.bodies)-1]
	if r.err != nil {
		return nil, r.err
	}

	returned := &body{Reader: strings.NewReader("payload the retry never reads")}
	s.sent = append(s.sent, returned)
	return &http.Response{
		StatusCode: r.status,
		Header:     r.header,
		Body:       returned,
		Request:    req,
	}, nil
}

// calls is how many attempts reached the far side.
func (s *server) calls() int { return len(s.bodies) }

var errUnscripted = errors.New("the script ran out of replies")

// body tracks whether a response was closed. A retried response nobody closes
// leaks its connection, and no assertion about status codes would notice.
type body struct {
	*strings.Reader
	closed bool
}

func (b *body) Close() error {
	b.closed = true
	return nil
}

// recorder is a Sleeper that returns at once and remembers what it was asked to
// wait, so a test names a delay instead of taking one.
type recorder struct {
	waited []time.Duration
}

func (r *recorder) sleep(ctx context.Context, d time.Duration) error {
	r.waited = append(r.waited, d)
	return ctx.Err()
}

func request(t *testing.T, ctx context.Context, payload string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.example/v1/messages", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if req.GetBody == nil {
		t.Fatal("http.NewRequestWithContext left GetBody nil for a strings.Reader, so this test " +
			"would be asserting the unrepeatable-body path by accident")
	}
	return req
}
