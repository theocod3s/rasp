package anthropic

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"go.uber.org/goleak"

	"github.com/theocod3s/rasp/internal/llm"
)

// TestMain runs the leak detector. Every stream here holds an HTTP body open,
// and a stream abandoned by its consumer is the case most likely to leave one.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fake is a client pointed at a server that replays one recorded response and
// keeps the request that asked for it. Recorded frames rather than a hand-built
// event slice, because the wire format and the neutral message have to stay two
// independent things to compare (design §3.1a).
type fake struct {
	*Client

	mu      sync.Mutex
	request []byte
}

func replay(t *testing.T, fixture string) *fake {
	t.Helper()
	body := fixtureBytes(t, fixture)

	f := &fake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		f.mu.Lock()
		f.request = sent
		f.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	f.Client = New(Config{APIKey: "test-key", BaseURL: srv.URL})
	return f
}

// respond serves one canned status and body, for the failures that never reach
// the SSE decoder at all.
func respond(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(Config{APIKey: "test-key", BaseURL: srv.URL})
}

// sent decodes the request the adapter put on the wire.
func (f *fake) sent(t *testing.T) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.request) == 0 {
		t.Fatal("the adapter sent no request")
	}
	var out map[string]any
	if err := json.Unmarshal(f.request, &out); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	return out
}

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	if len(body) == 0 {
		t.Fatalf("fixture %s is empty; a stream with no frames would test nothing", name)
	}
	return body
}

// ask is the request the fixtures were recorded against. Its content is
// irrelevant to every assertion but the ones in request_test.go.
func ask() llm.Request {
	return llm.Request{
		Model:     "claude-opus-5",
		MaxTokens: 1024,
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: []llm.Block{{Type: llm.BlockText, Text: "read auth.go"}},
		}},
	}
}

func types(events []llm.Event) []llm.EventType {
	out := make([]llm.EventType, len(events))
	for i, ev := range events {
		out[i] = ev.Type
	}
	return out
}

func last(t *testing.T, events []llm.Event) llm.Event {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("stream yielded no events")
	}
	return events[len(events)-1]
}
