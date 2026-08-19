package openaicompat

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	sdk "github.com/openai/openai-go/v3"
	"go.uber.org/goleak"

	"github.com/theocod3s/rasp/internal/llm"
)

// TestMain runs the leak detector. Every stream here holds an HTTP body open, and
// a stream abandoned by its consumer is the case most likely to leave one.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fake is a client pointed at a server that replays one recorded response and
// keeps the request that asked for it. Tests enter at the socket so that the SSE
// decoder, AddChunk and the projection all run; a hand-built []llm.Event would
// assert against events the test itself constructed.
type fake struct {
	*Client

	mu      sync.Mutex
	request []byte
	headers http.Header
}

func replay(t *testing.T, fixture string) *fake {
	t.Helper()
	return replayAs(t, fixture, Config{ProviderID: testProvider, APIKey: "test-key"})
}

// replayAs is replay with the credentials under test's control. Config.BaseURL is
// overwritten with the server's.
func replayAs(t *testing.T, fixture string, cfg Config) *fake {
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
		f.headers = r.Header.Clone()
		f.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	cfg.BaseURL = srv.URL
	f.Client = New(cfg)
	return f
}

// noAmbientCredentials clears what the SDK's own resolution would otherwise pick
// up, so a test observes what the adapter contributes rather than the shell it was
// run from.
func noAmbientCredentials(t *testing.T) {
	t.Helper()
	for _, key := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL"} {
		if _, ok := os.LookupEnv(key); ok {
			t.Setenv(key, "")
			os.Unsetenv(key) // t.Setenv registers the restore; Setenv cannot unset
		}
	}
}

// respond serves one canned status and body, for the failures that never reach the
// SSE decoder at all.
func respond(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(Config{ProviderID: testProvider, APIKey: "test-key", BaseURL: srv.URL})
}

// refuse replies 429 to every request and counts them, so a retry the adapter was
// not supposed to make is visible as a number rather than as latency.
func refuse(t *testing.T) (*Client, func() int) {
	t.Helper()
	var (
		mu sync.Mutex
		n  int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"rate_limit_error"}}`))
	}))
	t.Cleanup(srv.Close)

	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
	return New(Config{ProviderID: testProvider, APIKey: "test-key", BaseURL: srv.URL}), count
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

// maybeHeaders returns the headers of the request that arrived, or nil if the
// adapter never sent one.
func (f *fake) maybeHeaders() http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.headers
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

// testProvider is the name the fixtures are replayed under. Not "openai": the
// point of this adapter is that the endpoint is somebody else's.
const testProvider = "openrouter"

// ask is the request the fixtures were recorded against. Its content is irrelevant
// to every assertion but the ones in request_test.go.
func ask() llm.Request {
	return llm.Request{
		Model:     "openai/gpt-4o-mini",
		MaxTokens: 1024,
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: []llm.Block{{Type: llm.BlockText, Text: "read auth.go"}},
		}},
	}
}

// askWithTools is ask() with the one tool the fixtures were recorded against.
func askWithTools() llm.Request {
	req := ask()
	req.Tools = []llm.ToolSpec{{
		Name:        "read",
		Description: "Read a span of a file",
		Schema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"path": map[string]any{"type": "string", "description": "Which file"}},
			"required":             []any{"path"},
			"additionalProperties": false,
		},
	}}
	return req
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

// announced is the ids the stream announced, in order. A missing announcement is
// silence, and silence reads as a pass, so tests name the whole list.
func announced(events []llm.Event) []string {
	var out []string
	for _, ev := range events {
		if ev.Type == llm.EventToolCall {
			out = append(out, ev.ToolCall.ID)
		}
	}
	return out
}

// announcement is the EventToolCall for id, and a failure if the stream never
// announced one.
func announcement(t *testing.T, events []llm.Event, id string) *llm.ToolCall {
	t.Helper()
	for _, ev := range events {
		if ev.Type == llm.EventToolCall && ev.ToolCall.ID == id {
			return ev.ToolCall
		}
	}
	t.Fatalf("no EventToolCall announced %q; the loop dispatches from the event, so a call only in "+
		"the message is one nothing runs", id)
	return nil
}

// wireMessage is one message as the request carries it. Read out of the bytes
// rather than off the params struct: the union's zero fields are invisible from
// the Go side, and what the API sees is the JSON.
type wireMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

func decodeMessages(t *testing.T, params sdk.ChatCompletionNewParams) []wireMessage {
	t.Helper()
	var decoded struct {
		Messages []wireMessage `json:"messages"`
	}
	body := requestBody(t, params)
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decoding the request body: %v", err)
	}
	if len(decoded.Messages) == 0 {
		t.Fatalf("the request carried no messages, so nothing was examined: %s", body)
	}
	return decoded.Messages
}

func roles(messages []wireMessage) []string {
	out := make([]string, len(messages))
	for i, msg := range messages {
		out[i] = msg.Role
	}
	return out
}

func requestBody(t *testing.T, params sdk.ChatCompletionNewParams) string {
	t.Helper()
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshalling the params: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("the params marshalled to nothing, so every check against them would pass")
	}
	return string(body)
}

func marshalled(t *testing.T, v any) string {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling %v: %v", v, err)
	}
	return string(out)
}
