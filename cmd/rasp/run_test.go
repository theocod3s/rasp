package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/config"
)

func TestRunRequiresAPrompt(t *testing.T) {
	unreachableModel(t)

	stdout, _, err := run(t, "run")
	// The flag is named and the word is cobra's, so a requirement that quietly
	// stopped being declared shows up as this test rather than as a turn spent
	// asking the model nothing.
	if err == nil || !strings.Contains(err.Error(), "required") || !strings.Contains(err.Error(), promptFlag) {
		t.Fatalf("`run` with no prompt failed with %v, want a required-flag error naming --%s",
			err, promptFlag)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing but model output", stdout)
	}
}

// TestRunRejectsAnEmptyPrompt closes the gap the required flag leaves: cobra is
// satisfied by `-p ""`, which is a prompt in name only.
func TestRunRejectsAnEmptyPrompt(t *testing.T) {
	unreachableModel(t)

	for _, prompt := range []string{"", "   \n"} {
		stdout, _, err := run(t, "run", "-p", prompt)
		// Naming the flag, not merely failing: every later step fails on this
		// config too, so `err != nil` would hold with the check gone.
		if err == nil || !strings.Contains(err.Error(), promptFlag) {
			t.Errorf("`run -p %q` failed with %v, want an error naming --%s", prompt, err, promptFlag)
		}
		if stdout != "" {
			t.Errorf("`run -p %q` wrote %q to stdout", prompt, stdout)
		}
	}
}

// replayServer answers any request with one streamed text reply.
func replayServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, replyStream)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// unreachableModel points the configuration at a server that fails the test if
// anything reaches it, so "the prompt never got sent" is asserted rather than
// assumed.
func unreachableModel(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a request reached the model")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	projectConfig(t, fmt.Sprintf(`{
	  "model": "anthropic/claude-opus-5",
	  "providers": {"anthropic": {"api_key": "test-key", "base_url": %q}}
	}`, srv.URL))
}

// TestRunExitsOneWithNothingOnStdout runs the real entry point rather than the
// command tree, because the exit status is what a script reads and only execute
// produces one.
func TestRunExitsOneWithNothingOnStdout(t *testing.T) {
	t.Setenv("RASP_LOG_FILE", filepath.Join(t.TempDir(), "rasp.log"))
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	// A model with no credential: projectConfig clears rasp's environment layer,
	// and this clears the one the SDK reads that rasp knows nothing about.
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	// And an endpoint that fails the test if anything reaches it, because the SDK
	// resolves credentials from more places than rasp does — a profile file and
	// workload federation among them. Without this the test billed a live request
	// to api.anthropic.com for anyone with a gateway token exported.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a request left the machine; this run should have failed before one")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	projectConfig(t, fmt.Sprintf(`{
	  "model": "anthropic/claude-opus-5",
	  "providers": {"anthropic": {"base_url": %q}}
	}`, srv.URL))

	stdout, readStdout := captureFile(t, "stdout")
	stderr, readStderr := captureFile(t, "stderr")

	code := func() int {
		realOut, realErr := os.Stdout, os.Stderr
		os.Stdout, os.Stderr = stdout, stderr
		defer func() { os.Stdout, os.Stderr = realOut, realErr }()
		return execute([]string{"run", "-p", "hello"})
	}()

	if code != 1 {
		t.Errorf("exit status %d, want 1", code)
	}
	if printed := readStdout(); printed != "" {
		t.Errorf("stdout = %q; a failure belongs on stderr", printed)
	}
	// The diagnosis, not merely something: a run that failed silently leaves the
	// user with an exit status and nowhere to look. The adapter names itself in
	// every failure it reports, which a cobra usage dump or an empty stderr does
	// not.
	reported := readStderr()
	if !strings.Contains(reported, "anthropic:") {
		t.Errorf("stderr = %q, want the reason the run failed", reported)
	}
	// The last line of defence, and the one that survives the guards above being
	// deleted: a unit test naming the production API has already talked to it.
	if strings.Contains(reported, "api.anthropic.com") {
		t.Errorf("the run reached the real API: %q", reported)
	}
}

// captureFile redirects one of the process's standard streams to a file and
// returns a reader for what landed in it.
func captureFile(t *testing.T, name string) (*os.File, func() string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	t.Cleanup(func() { file.Close() })

	return file, func() string {
		captured, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		return string(captured)
	}
}

// TestRunStreamsTheReplyToStdout is the whole wiring against a server speaking
// Anthropic's stream format: config, adapter, request, and the reply arriving as
// plain text with nothing around it.
func TestRunStreamsTheReplyToStdout(t *testing.T) {
	sent := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request body: %v", err)
		}
		sent <- body

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, replyStream)
	}))
	defer srv.Close()

	projectConfig(t, fmt.Sprintf(`{
	  "model": "anthropic/claude-opus-5",
	  "providers": {"anthropic": {"api_key": "test-key", "base_url": %q}}
	}`, srv.URL))

	stdout, stderr, err := run(t, "run", "-p", "why is the sky blue?")
	if err != nil {
		t.Fatalf("run: %v (stderr %q)", err, stderr)
	}
	if stdout != "Rayleigh scattering.\n" {
		t.Errorf("stdout = %q, want the reply and nothing else", stdout)
	}

	var request struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(<-sent, &request); err != nil {
		t.Fatalf("decoding the request: %v", err)
	}
	// The provider half of `anthropic/claude-opus-5` chooses the adapter and is
	// not a name the API knows; sending it whole is a 404 per turn.
	if request.Model != "claude-opus-5" {
		t.Errorf("request model = %q, want the id with no provider prefix", request.Model)
	}
}

// TestRunSendsTheConfiguredOutputCap is the whole point of the setting: hitting
// the cap fails the run, so the number the user wrote has to be the number on the
// wire. Read off the request body rather than the runner, because every layer
// between the two — resolver, adapter, SDK — is somewhere it could be replaced by
// a default nobody chose.
func TestRunSendsTheConfiguredOutputCap(t *testing.T) {
	tests := []struct {
		name    string
		setting string
		want    int64
	}{
		// 4321 is neither a default anything in the tree uses nor a round number an
		// SDK might supply on its own. The 16384 below is design §10's documented
		// default, written out rather than read back from the resolver, so raising
		// it stays a deliberate edit rather than something a test agrees to.
		{name: "configured", setting: `"max_output_tokens": 4321,`, want: 4321},
		{name: "unset", setting: "", want: 16384},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sent := make(chan []byte, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("reading the request body: %v", err)
				}
				sent <- body

				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, replyStream)
			}))
			defer srv.Close()

			projectConfig(t, fmt.Sprintf(`{
			  "model": "anthropic/claude-opus-5",
			  %s
			  "providers": {"anthropic": {"api_key": "test-key", "base_url": %q}}
			}`, tc.setting, srv.URL))

			if _, stderr, err := run(t, "run", "-p", "hi"); err != nil {
				t.Fatalf("run: %v (stderr %q)", err, stderr)
			}

			var request struct {
				MaxTokens int64 `json:"max_tokens"`
			}
			if err := json.Unmarshal(<-sent, &request); err != nil {
				t.Fatalf("decoding the request: %v", err)
			}
			if request.MaxTokens != tc.want {
				t.Errorf("request max_tokens = %d, want %d", request.MaxTokens, tc.want)
			}
		})
	}
}

// TestRunReportsConfigWarningsOnStderr: a misspelt setting is dropped silently
// by the resolver, and `rasp run` is the command where the user would otherwise
// meet that as an answer produced under settings they thought they had changed.
func TestRunReportsConfigWarningsOnStderr(t *testing.T) {
	srv := replayServer(t)
	projectConfig(t, fmt.Sprintf(`{
	  "model": "anthropic/claude-opus-5",
	  "modle": "typo/here",
	  "providers": {"anthropic": {"api_key": "test-key", "base_url": %q}}
	}`, srv.URL))

	stdout, stderr, err := run(t, "run", "-p", "hi")
	if err != nil {
		t.Fatalf("run: %v (stderr %q)", err, stderr)
	}
	if !strings.Contains(stderr, "modle") {
		t.Errorf("stderr = %q, want the ignored setting named", stderr)
	}
	// And the warning did not land in what a script reads.
	if stdout != "Rayleigh scattering.\n" {
		t.Errorf("stdout = %q, want the reply and nothing else", stdout)
	}
}

// TestRunExpandsWhatTheConfigFileHolds: a config value is a recipe, not the
// thing itself. Sent as written, the credential is a 401 on every run and the
// endpoint is a request naming a URL nobody typed — and `config check` would
// still look right, because it hides the credential either way.
func TestRunExpandsWhatTheConfigFileHolds(t *testing.T) {
	t.Setenv("RASP_TEST_CREDENTIAL", "sk-expanded")

	headers := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, replyStream)
	}))
	defer srv.Close()

	// The endpoint goes through the grammar too, so a request reaching this
	// server at all is the assertion that it did.
	t.Setenv("RASP_TEST_ENDPOINT", srv.URL)
	projectConfig(t, `{
	  "model": "anthropic/claude-opus-5",
	  "providers": {"anthropic": {
	    "api_key": "${RASP_TEST_CREDENTIAL}",
	    "base_url": "${RASP_TEST_ENDPOINT}"
	  }}
	}`)

	if _, stderr, err := run(t, "run", "-p", "hi"); err != nil {
		t.Fatalf("run: %v (stderr %q)", err, stderr)
	}
	if got := (<-headers).Get("X-Api-Key"); got != "sk-expanded" {
		t.Errorf("X-Api-Key = %q, want the expanded credential", got)
	}
}

// TestRunAnswersFromAnOpenAICompatibleEndpoint is the second adapter reached the
// way a user reaches it: a provider name with a base URL, and no code path of its
// own past the prefix. The reply arriving on stdout is what says the wiring holds
// end to end — config, credential expansion, the adapter, and the runner.
func TestRunAnswersFromAnOpenAICompatibleEndpoint(t *testing.T) {
	sent := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request: %v", err)
		}
		sent <- body
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, openAIReplyStream)
	}))
	defer srv.Close()

	projectConfig(t, fmt.Sprintf(`{
	  "model": "openrouter/openai/gpt-4o-mini",
	  "providers": {"openrouter": {"api_key": "test-key", "base_url": %q}}
	}`, srv.URL))

	stdout, stderr, err := run(t, "run", "-p", "why is the sky blue")
	if err != nil {
		t.Fatalf("run: %v (stderr %q)", err, stderr)
	}
	if stdout != "Rayleigh scattering.\n" {
		t.Errorf("stdout = %q, want the reply and nothing else", stdout)
	}

	var request struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(<-sent, &request); err != nil {
		t.Fatalf("decoding the request: %v", err)
	}
	// The provider prefix chose the adapter and is not a name this API knows.
	if request.Model != "openai/gpt-4o-mini" {
		t.Errorf("model on the wire = %q, want the id with the provider prefix taken off", request.Model)
	}
}

func TestBuildProviderRejectsWhatItCannotServe(t *testing.T) {
	cfg := config.Config{Model: "claude-opus-5"}

	_, _, err := buildProvider(t.Context(), &config.Result{Config: cfg})
	if err == nil {
		t.Fatalf("buildProvider(%+v) succeeded", cfg)
	}
	if !strings.Contains(err.Error(), "provider/id") {
		t.Errorf("error %q does not say what the model id should have looked like", err)
	}
}

// TestBuildProviderRoutesOnTheProviderPrefix: `anthropic` has an adapter of its
// own and every other name is an OpenAI-compatible endpoint, so the prefix is the
// whole routing decision. The model ids are the ones that make it worth asserting
// — `openrouter/anthropic/claude-sonnet-4.5` cuts at the FIRST slash, and the rest
// goes on the wire as OpenRouter's own id rather than as a second provider name.
func TestBuildProviderRoutesOnTheProviderPrefix(t *testing.T) {
	for _, tc := range []struct {
		model     string
		wantID    string
		wantModel string
	}{
		{"anthropic/claude-opus-5", "anthropic", "claude-opus-5"},
		{"openrouter/anthropic/claude-sonnet-4.5", "openrouter", "anthropic/claude-sonnet-4.5"},
		{"ollama/qwen2.5-coder:1.5b", "ollama", "qwen2.5-coder:1.5b"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			res := &config.Result{Config: config.Config{Model: tc.model}}

			provider, model, err := buildProvider(t.Context(), res)
			if err != nil {
				t.Fatalf("buildProvider: %v", err)
			}
			if provider.ID() != tc.wantID {
				t.Errorf("provider = %q, want %q", provider.ID(), tc.wantID)
			}
			if model != tc.wantModel {
				t.Errorf("model on the wire = %q, want %q", model, tc.wantModel)
			}
		})
	}
}

// replyStream is Anthropic's SSE shape, trimmed to what a text answer needs.
const replyStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-opus-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Rayleigh"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" scattering."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":8}}

event: message_stop
data: {"type":"message_stop"}
`

// openAIReplyStream is the OpenAI-compatible SSE shape, saying the same thing.
const openAIReplyStream = `data: {"id":"gen-1","object":"chat.completion.chunk","created":1787108071,"model":"openai/gpt-4o-mini","choices":[{"index":0,"delta":{"content":"Rayleigh","role":"assistant"},"finish_reason":null}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1787108071,"model":"openai/gpt-4o-mini","choices":[{"index":0,"delta":{"content":" scattering.","role":"assistant"},"finish_reason":null}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1787108071,"model":"openai/gpt-4o-mini","choices":[{"index":0,"delta":{"content":"","role":"assistant"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}

data: [DONE]
`
