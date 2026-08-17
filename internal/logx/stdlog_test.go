package logx_test

import (
	"bytes"
	"context"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/anthropic"
	"github.com/theocod3s/rasp/internal/logx"
)

// This file drives the case that motivated logx claiming the standard sinks:
// anthropic-sdk-go writes to Go's standard log from inside the first request,
// which is the middle of a turn as far as the display is concerned. The
// provider adapter appears here — rather than logx appearing in a provider
// test — because the subject is who owns the sinks; the SDK is the witness.
//
// The check runs in a child process. Go's log package captured os.Stderr at its
// own init, so swapping the variable intercepts nothing it writes, and only a
// real process boundary shows what a terminal would have seen.
const (
	// helperMode names the sink arrangement the child should set up, and its
	// presence is what tells the child it is one.
	helperMode = "RASP_TEST_SINK_MODE"

	// helperRan is the path the child touches once it has finished. A -test.run
	// pattern matching nothing exits 0 having run nothing, which is
	// indistinguishable from a clean terminal.
	helperRan = "RASP_TEST_HELPER_RAN"

	helperTest = "TestSDKWritesFromInsideTheFirstRequest"
)

const (
	// modeAdopted is a process that called logx.Init, modeBare one that never
	// did, and modeStock the arrangement Go hands every process and rasp shipped
	// with until logx took it — the control.
	modeAdopted = "adopted"
	modeBare    = "bare"
	modeStock   = "stock"
)

// sdkMarkers are the prefixes of the two lines the SDK emits for a profile
// shadowed by an explicit key. Prefixes rather than whole lines, because the
// wording is the SDK's to change and the sink is what is under test.
var sdkMarkers = []string{"anthropic-sdk-go/config:", "anthropic-sdk-go/auth:"}

// profileToken is the credential the fixture profile carries. Nothing should
// ever print or log it; the SDK names the field it did not recognise, not its
// value, and this is what proves that stays true.
const profileToken = "not-a-real-access-token"

func TestProviderSDKOutputNeverReachesTheTerminal(t *testing.T) {
	cases := []struct {
		name       string
		mode       string
		onTerminal bool
		inFile     bool
	}{
		{name: "logx owns the standard sinks", mode: modeAdopted, inFile: true},
		{name: "before Init, output is discarded", mode: modeBare},
		{name: "stock sinks put it on the terminal", mode: modeStock, onTerminal: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runHelper(t, tc.mode)

			if got.stdout != "" {
				t.Errorf("the child wrote to stdout, which belongs to the UI: %q", got.stdout)
			}
			assertMarkers(t, "stderr", got.stderr, tc.onTerminal)
			assertMarkers(t, "the log file", got.logged, tc.inFile)

			for name, stream := range map[string]string{
				"stdout": got.stdout, "stderr": got.stderr, "the log file": got.logged,
			} {
				if strings.Contains(stream, profileToken) {
					t.Errorf("the profile's access token reached %s", name)
				}
			}
		})
	}
}

// TestSDKWritesFromInsideTheFirstRequest is the child half of the test above,
// and skips unless that test launched it.
func TestSDKWritesFromInsideTheFirstRequest(t *testing.T) {
	mode, ok := os.LookupEnv(helperMode)
	if !ok {
		t.Skipf("child process of TestProviderSDKOutputNeverReachesTheTerminal, which sets %s", helperMode)
	}

	switch mode {
	case modeAdopted:
		lg := logx.Init(nil)
		defer lg.Close()
	case modeBare:
		// Nothing: importing logx has already pointed the sinks at nothing.
	case modeStock:
		// Restored by hand, because importing logx at all takes it away. This is
		// the writer every Go process starts with.
		log.SetOutput(os.Stderr)
	default:
		t.Fatalf("unknown mode %q", mode)
	}

	var served atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, turn)
	}))
	defer srv.Close()

	client := anthropic.New(anthropic.Config{APIKey: "test-key", BaseURL: srv.URL})
	for range client.Stream(context.Background(), llm.Request{
		Model:     "claude-opus-5",
		MaxTokens: 1024,
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: []llm.Block{{Type: llm.BlockText, Text: "hello"}},
		}},
	}) {
	}

	// The warning the parent looks for is emitted by a request middleware, so a
	// request that never left would make every mode agree on silence.
	if n := served.Load(); n != 1 {
		t.Fatalf("the server saw %d requests, want 1", n)
	}

	path := os.Getenv(helperRan)
	if err := os.WriteFile(path, []byte(mode), 0o600); err != nil {
		t.Fatalf("marking the run at %s: %v", path, err)
	}
}

// TestRedirectedRecordsAreRedacted covers the sinks logx adopts on the way in:
// they reach the file through the same handler as everything else, so a
// credential written to slog.Default is dropped there too.
func TestRedirectedRecordsAreRedacted(t *testing.T) {
	_, path := logTo(t, nil)
	slog.Default().Info("adapter", "api_key", "sk-secret")
	log.Print("a dependency's line")

	got := records(t, path)
	if len(got) != 2 {
		t.Fatalf("logged %v, want the slog record and the standard log line", got)
	}
	if got[0]["api_key"] != logx.Redacted {
		t.Errorf("api_key is %v, want %q", got[0]["api_key"], logx.Redacted)
	}
	if got[1]["msg"] != "a dependency's line" {
		t.Errorf("the standard log line arrived as %v", got[1])
	}
	if raw := read(t, path); strings.Contains(raw, "sk-secret") {
		t.Error("the credential reached the file")
	}
}

// helperResult is what a child process left behind: what a terminal would have
// shown, and what the log file holds.
type helperResult struct{ stdout, stderr, logged string }

func runHelper(t *testing.T, mode string) helperResult {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating this test binary: %v", err)
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "rasp.log")
	ranPath := filepath.Join(dir, "ran")

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, "-test.run", "^"+helperTest+"$")
	cmd.Env = append(childEnv(),
		helperMode+"="+mode,
		helperRan+"="+ranPath,
		"RASP_LOG_FILE="+logPath,
		"ANTHROPIC_CONFIG_DIR="+profileDir(t),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("child process: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(ranPath); err != nil {
		t.Fatalf("%s never ran, so a silent terminal proves nothing: %v", helperTest, err)
	}

	return helperResult{
		stdout: withoutVerdict(stdout.String()),
		stderr: stderr.String(),
		logged: maybeRead(t, logPath),
	}
}

// withoutVerdict drops the line a test binary prints about itself, which is the
// harness talking rather than the process under test.
func withoutVerdict(out string) string {
	return strings.TrimSuffix(out, "PASS\n")
}

// childEnv is this process's environment without the variables either side of
// the boundary reads. A developer's own ANTHROPIC_API_KEY would otherwise
// decide which credential source the SDK picks, and so whether it warns at all.
func childEnv() []string {
	var kept []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "RASP_") {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

// profileDir writes the configuration `ant auth login` leaves behind — a
// profile holding an OAuth token — and returns the directory to point
// ANTHROPIC_CONFIG_DIR at. access_token is not a field the SDK knows, which is
// what draws the first of the two lines.
func profileDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "configs", "default.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	profile := `{"version":"1.0","authentication":{"type":"user_oauth","access_token":"` + profileToken + `"}}`
	if err := os.WriteFile(path, []byte(profile), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return dir
}

func assertMarkers(t *testing.T, where, got string, want bool) {
	t.Helper()
	for _, marker := range sdkMarkers {
		switch {
		case want && !strings.Contains(got, marker):
			t.Errorf("%s does not carry %q; if the SDK stopped writing it, this test needs "+
				"a new witness rather than a passing grade:\n%s", where, marker, got)
		case !want && strings.Contains(got, marker):
			t.Errorf("%s carries %q:\n%s", where, marker, got)
		}
	}
}

func maybeRead(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(contents)
}

// turn is a complete, minimal streamed response. Its content is irrelevant: the
// request has to reach the server, and the stream has to end.
const turn = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-opus-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`
