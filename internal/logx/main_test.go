package logx_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/theocod3s/rasp/internal/logx"
)

// TestMain runs the leak detector over the package. Init hands back a file the
// caller closes; a goroutine behind a future writer would be the thing that
// outlives the process's last turn.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// env is a stand-in for the process environment, so no test has to mutate the
// real one.
type env map[string]string

func (e env) lookup(key string) (string, bool) {
	val, ok := e[key]
	return val, ok
}

// logTo runs Init against a log file inside a fresh directory and returns the
// Log alongside that path. Each seed runs against that path first, for tests
// that need something already on disk.
func logTo(t *testing.T, vars env, seeds ...func(*testing.T, string)) (*logx.Log, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rasp.log")
	if vars == nil {
		vars = env{}
	}
	vars["RASP_LOG_FILE"] = path
	for _, seed := range seeds {
		seed(t, path)
	}

	lg := logx.Init(vars.lookup)
	t.Cleanup(func() {
		if err := lg.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if len(lg.Warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", lg.Warnings)
	}
	return lg, path
}

func read(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(contents)
}

// records parses the log file as JSON lines, failing when it holds none: a
// helper that examines nothing is the quietest pass there is.
func records(t *testing.T, path string) []map[string]any {
	t.Helper()
	var parsed []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(read(t, path)), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %v\n%s", err, line)
		}
		parsed = append(parsed, rec)
	}
	if len(parsed) == 0 {
		t.Fatalf("%s holds no records", path)
	}
	return parsed
}
