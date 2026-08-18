package tool_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/theocod3s/rasp/internal/tool"
)

// panicSite is a named frame the recovered stack must contain: a guard that
// captured its own stack after unwinding would still produce something that
// looks like a trace, and only a frame from inside the tool proves otherwise.
func panicSite(value any) (tool.Result, error) { panic(value) }

// fake is the Tool the guard runs. It has its own type rather than reusing the
// shared stub because these tests need a Name that panics.
type fake struct {
	name      string
	namePanic any
	run       func(context.Context, json.RawMessage) (tool.Result, error)
}

func (f fake) Name() string {
	if f.namePanic != nil {
		panic(f.namePanic)
	}
	return f.name
}

func (f fake) Description() string    { return "a tool the guard dispatches" }
func (f fake) Schema() map[string]any { return map[string]any{"type": "object"} }

func (f fake) Run(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	return f.run(ctx, raw)
}

func panicking(name string, value any) fake {
	return fake{name: name, run: func(context.Context, json.RawMessage) (tool.Result, error) {
		return panicSite(value)
	}}
}

// captureLog points the process default at a buffer, which is where the guard
// writes: logx hands slog's default the log file, so a package that only calls
// slog reaches the file without importing it (design §2).
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func onlyRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if buf.Len() == 0 || len(lines) != 1 {
		t.Fatalf("want exactly one log record, got %d:\n%s", buf.Len(), buf.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("log record is not JSON: %v", err)
	}
	return record
}

func TestPanicBecomesAnErrorResult(t *testing.T) {
	captureLog(t)

	res, err := tool.RunSafely(context.Background(), panicking("bash", "boom"), nil)

	if err != nil {
		t.Fatalf("the guard returned the Go error %v; a recovered panic is a failed call the model adapts to, and a Go error ends the turn instead", err)
	}
	if !res.IsError {
		t.Error("a panicking tool came back with IsError false, so the model is told it succeeded")
	}
	if res.Content != "tool panicked: boom" {
		t.Errorf("Content is %q, want the recovered value; the model has nothing to adapt to otherwise", res.Content)
	}
}

func TestStackReachesTheLogNotTheModel(t *testing.T) {
	buf := captureLog(t)

	res, _ := tool.RunSafely(context.Background(), panicking("bash", "boom"), nil)

	record := onlyRecord(t, buf)
	if record["msg"] != "tool panicked" || record["level"] != "ERROR" {
		t.Errorf("logged %v at %v, want a tool panicked record at ERROR", record["msg"], record["level"])
	}
	if record["tool"] != "bash" {
		t.Errorf("the record names tool %v, want bash; a stack with no tool on it does not say what to fix", record["tool"])
	}
	stack, ok := record["stack"].(string)
	if !ok {
		t.Fatalf("the record carries no stack:\n%s", buf.String())
	}
	if !strings.Contains(stack, "panicSite") {
		t.Errorf("the logged stack has no frame from inside the tool, so it was taken after unwinding:\n%s", stack)
	}

	if res.Content != "tool panicked: boom" {
		t.Fatalf("Content is %q; the checks below only mean something against the real message", res.Content)
	}
	for _, leaked := range []string{"panicSite", "goroutine", "runtime/debug", ".go:"} {
		if strings.Contains(res.Content, leaked) {
			t.Errorf("Content carries %q, so the stack is being spent as tokens the model cannot act on", leaked)
		}
	}
}

var errNoShellHere = errors.New("no shell on PATH")

func TestAToolThatDidNotPanicPassesThroughUnchanged(t *testing.T) {
	details := &struct{ Code int }{Code: 1}

	for name, run := range map[string]func(context.Context, json.RawMessage) (tool.Result, error){
		"ran and failed": func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{Content: "exit status 1", IsError: true, Title: "go test", Details: details}, nil
		},
		"could not run": func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{}, errNoShellHere
		},
	} {
		t.Run(name, func(t *testing.T) {
			buf := captureLog(t)

			// The expectation is whatever the tool itself returns, so the check is
			// that the guard is transparent rather than that it produces some
			// second literal written out here.
			want, wantErr := run(context.Background(), nil)

			res, err := tool.RunSafely(context.Background(), fake{name: "bash", run: run}, nil)

			if !errors.Is(err, wantErr) {
				t.Fatalf("got error %v, want %v: the guard is for panics and must neither swallow the error a tool returns nor invent one", err, wantErr)
			}
			if res != want {
				t.Errorf("got %+v, want %+v: every field of a result the tool built has to survive the guard", res, want)
			}
			if buf.Len() != 0 {
				t.Errorf("a call that never panicked logged:\n%s", buf.String())
			}
		})
	}
}

func TestTheTurnContinuesAfterAToolPanics(t *testing.T) {
	captureLog(t)

	batch := []tool.Tool{
		fake{name: "read", run: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{Content: "package tool"}, nil
		}},
		panicking("edit", "index out of range [3] with length 0"),
		fake{name: "grep", run: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.Result{Content: "3 matches"}, nil
		}},
	}

	// Dispatched the way the loop will: concurrently, landing by index. An
	// unrecovered panic in any of these goroutines takes the process with it, so
	// this is where "the process survives" is actually observable.
	results := make([]tool.Result, len(batch))
	var wg sync.WaitGroup
	for i, subject := range batch {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = tool.RunSafely(context.Background(), subject, nil)
		}()
	}
	wg.Wait()

	want := []tool.Result{
		{Content: "package tool"},
		{Content: "tool panicked: index out of range [3] with length 0", IsError: true},
		{Content: "3 matches"},
	}
	for i := range want {
		if results[i] != want[i] {
			t.Errorf("call %d came back as %+v, want %+v", i, results[i], want[i])
		}
	}
}

func TestPanicValuesTheModelStillReads(t *testing.T) {
	captureLog(t)

	for name, test := range map[string]struct {
		value any
		want  string
	}{
		"an error":       {value: errNoShellHere, want: "tool panicked: no shell on PATH"},
		"a struct":       {value: struct{ Path string }{Path: "/etc"}, want: "tool panicked: {/etc}"},
		"an int":         {value: 7, want: "tool panicked: 7"},
		"nothing at all": {value: nil, want: "tool panicked: panic called with nil argument"},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := tool.RunSafely(context.Background(), panicking("bash", test.value), nil)

			if err != nil {
				t.Fatalf("the guard returned the Go error %v", err)
			}
			if !res.IsError {
				t.Error("IsError is false, so the model is told the call succeeded")
			}
			if res.Content != test.want {
				t.Errorf("Content is %q, want %q", res.Content, test.want)
			}
		})
	}
}

// TestAPanicRecoveredAsNilIsStillAFailure covers the one shape where recover
// answers nil for a goroutine that did panic. The Content assertion is what
// keeps this test honest: without the setting in force the value would render as
// the 1.21+ error, and the test would pass while exercising nothing.
func TestAPanicRecoveredAsNilIsStillAFailure(t *testing.T) {
	t.Setenv("GODEBUG", "panicnil=1")
	captureLog(t)

	res, err := tool.RunSafely(context.Background(), panicking("bash", nil), nil)

	if err != nil {
		t.Fatalf("the guard returned the Go error %v", err)
	}
	if !res.IsError || res.Content == "" {
		t.Fatalf("got %+v, want a recovered error result: recover answers nil here for a goroutine that did panic, and reading that as success tells the model nothing happened", res)
	}
	if res.Content != "tool panicked: <nil>" {
		t.Fatalf("Content is %q, want the nil value rendered: GODEBUG=panicnil=1 is not in force, so this ran the ordinary path and proved nothing", res.Content)
	}
}

func TestAToolWhoseNamePanicsIsContained(t *testing.T) {
	buf := captureLog(t)

	res, err := tool.RunSafely(context.Background(), fake{namePanic: "no name"}, nil)

	if err != nil {
		t.Fatalf("the guard returned the Go error %v", err)
	}
	if !res.IsError || res.Content != "tool panicked: no name" {
		t.Errorf("got %+v, want a recovered error result: everything the guard touches on a tool is code the tool wrote", res)
	}
	if record := onlyRecord(t, buf); record["tool"] != "" {
		t.Errorf("the record names tool %v, but the name is what panicked", record["tool"])
	}
}
