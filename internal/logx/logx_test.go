package logx_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/logx"
)

func TestWritesJSONUnderXDGDataHome(t *testing.T) {
	dir := t.TempDir()
	lg := logx.Init(env{"XDG_DATA_HOME": dir}.lookup)
	defer lg.Close()

	want := filepath.Join(dir, "rasp", "logs", "rasp.log")
	if lg.Path != want {
		t.Fatalf("log path is %s, want %s", lg.Path, want)
	}
	lg.Logger.Info("hello", "count", 3)

	rec := records(t, want)[0]
	if rec["msg"] != "hello" || rec["level"] != "INFO" || rec["time"] == nil {
		t.Errorf("record is missing the JSON handler's fields: %v", rec)
	}
	if rec["count"] != float64(3) {
		t.Errorf("count is %v, want 3", rec["count"])
	}
}

func TestFallsBackToLocalShare(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	lg := logx.Init(env{}.lookup)
	defer lg.Close()

	want := filepath.Join(home, ".local", "share", "rasp", "logs", "rasp.log")
	if lg.Path != want {
		t.Fatalf("log path is %s, want %s", lg.Path, want)
	}
}

func TestLogFileOverridesTheDataDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elsewhere.log")
	dataHome := t.TempDir()

	lg := logx.Init(env{"XDG_DATA_HOME": dataHome, "RASP_LOG_FILE": path}.lookup)
	defer lg.Close()

	if lg.Path != path {
		t.Fatalf("log path is %s, want %s", lg.Path, path)
	}
	lg.Logger.Info("hello")
	records(t, path)

	if _, err := os.Stat(filepath.Join(dataHome, "rasp")); err == nil {
		t.Error("the data directory was used even though RASP_LOG_FILE was set")
	}
}

func TestLevel(t *testing.T) {
	cases := []struct {
		name    string
		level   string
		set     bool
		written []string
	}{
		// The first case leaves the variable unset, which is how it reaches a
		// user; an empty value is a different branch.
		{name: "unset is info", written: []string{"info", "warn"}},
		{name: "empty is info", level: "", set: true, written: []string{"info", "warn"}},
		{name: "debug", level: "debug", written: []string{"debug", "info", "warn"}},
		{name: "case insensitive", level: "DEBUG", written: []string{"debug", "info", "warn"}},
		{name: "warn", level: "warn", written: []string{"warn"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vars := env{}
			if tc.level != "" || tc.set {
				vars["RASP_LOG_LEVEL"] = tc.level
			}
			lg, path := logTo(t, vars)
			lg.Logger.Debug("debug")
			lg.Logger.Info("info")
			lg.Logger.Warn("warn")

			var got []string
			for _, rec := range records(t, path) {
				got = append(got, rec["msg"].(string))
			}
			if strings.Join(got, ",") != strings.Join(tc.written, ",") {
				t.Errorf("logged %v, want %v", got, tc.written)
			}
		})
	}
}

func TestUnreadableLevelWarnsAndUsesInfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rasp.log")
	lg := logx.Init(env{"RASP_LOG_FILE": path, "RASP_LOG_LEVEL": "chatty"}.lookup)
	defer lg.Close()

	if len(lg.Warnings) != 1 || !strings.Contains(lg.Warnings[0], "chatty") {
		t.Fatalf("warnings are %v, want one naming the value", lg.Warnings)
	}
	lg.Logger.Debug("debug")
	lg.Logger.Info("info")
	if got := records(t, path); len(got) != 1 || got[0]["msg"] != "info" {
		t.Errorf("logged %v, want info alone", got)
	}
}

// TestNothingReachesStdout guards design §2's rule that stdout belongs to the
// UI: a warning printed here would land in the middle of the TUI's frame.
func TestNothingReachesStdout(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		stdout, stderr := capture(t, func() {
			lg, _ := logTo(t, nil)
			lg.Logger.Info("hello", "api_key", "sk-ant-secret")
		})
		assertSilent(t, stdout, stderr)
	})

	t.Run("logging disabled", func(t *testing.T) {
		path := filepath.Join(blockedDir(t), "rasp.log")
		stdout, stderr := capture(t, func() {
			lg := logx.Init(env{"RASP_LOG_FILE": path}.lookup)
			lg.Logger.Info("hello")
			lg.Close()
		})
		assertSilent(t, stdout, stderr)
	})

	// The negative control: a capture that sees nothing is indistinguishable
	// from a package that writes nothing.
	t.Run("the capture works", func(t *testing.T) {
		stdout, stderr := capture(t, func() {
			fmt.Fprint(os.Stdout, "out")
			fmt.Fprint(os.Stderr, "err")
		})
		if stdout != "out" || stderr != "err" {
			t.Errorf("captured %q and %q; the checks above cannot fail", stdout, stderr)
		}
	})
}

// TestInitReadsTheProcessEnvironment covers the documented nil getenv, which
// every other test replaces — so the path the real caller takes is the one
// nothing else exercises.
func TestInitReadsTheProcessEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rasp.log")
	t.Setenv("RASP_LOG_FILE", path)

	lg := logx.Init(nil)
	defer lg.Close()

	if lg.Path != path {
		t.Fatalf("log path is %s, want %s", lg.Path, path)
	}
}

func TestUnwritableDirectoryWarnsOnceAndKeepsGoing(t *testing.T) {
	path := filepath.Join(blockedDir(t), "rasp.log")
	lg := logx.Init(env{"RASP_LOG_FILE": path}.lookup)

	if len(lg.Warnings) != 1 {
		t.Fatalf("warnings are %v, want exactly one", lg.Warnings)
	}
	if lg.Path != "" {
		t.Errorf("Path is %s, want empty when logging is off", lg.Path)
	}
	if lg.Logger == nil {
		t.Fatal("Logger is nil, so every call site would panic")
	}

	lg.Logger.Info("first")
	lg.Logger.Info("second")
	if len(lg.Warnings) != 1 {
		t.Errorf("logging added warnings: %v", lg.Warnings)
	}
	if err := lg.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestCloseStopsWriting is what makes Close observable from outside: slog
// swallows a handler's write errors, so a Close that closed nothing would be
// invisible except as a file descriptor held for the life of the process.
func TestCloseStopsWriting(t *testing.T) {
	lg, path := logTo(t, nil)
	lg.Logger.Info("before")
	if err := lg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	lg.Logger.Info("after")

	got := records(t, path)
	if len(got) != 1 || got[0]["msg"] != "before" {
		t.Errorf("records after Close: %v", got)
	}
}

func TestRotation(t *testing.T) {
	// 10 MB is the documented threshold, spelled out rather than read from the
	// package so a change to the constant fails here.
	const limit = 10 << 20
	const marker = "old log line\n"

	t.Run("over the limit", func(t *testing.T) {
		lg, path := logTo(t, nil, sized(limit+1, marker))
		lg.Logger.Info("fresh")

		if head := readHead(t, path+".1", len(marker)); head != marker {
			t.Errorf("rasp.log.1 starts with %q, want the old log's first line", head)
		}
		if size := size(t, path+".1"); size != limit+1 {
			t.Errorf("rasp.log.1 is %d bytes, want the oversized log's %d", size, limit+1)
		}
		if got := read(t, path); strings.Contains(got, marker) {
			t.Error("the fresh log still holds the old log's contents")
		}
		if rec := records(t, path); rec[0]["msg"] != "fresh" {
			t.Errorf("the fresh log opens with %v, want the record written after Init", rec[0])
		}
	})

	t.Run("at the limit", func(t *testing.T) {
		lg, path := logTo(t, nil, sized(limit, marker))
		lg.Logger.Info("appended")

		if _, err := os.Stat(path + ".1"); err == nil {
			t.Error("a log at exactly the limit was rotated")
		}
		if head := readHead(t, path, len(marker)); head != marker {
			t.Errorf("the log starts with %q, want the existing contents kept", head)
		}
		// records() cannot read this file — it opens with the seeded hole — so
		// the appended record is checked directly rather than not at all.
		if !strings.Contains(read(t, path), `"msg":"appended"`) {
			t.Error("the record logged after Init never reached the file")
		}
	})

	t.Run("the previous rotation is overwritten", func(t *testing.T) {
		lg, path := logTo(t, nil, sized(limit+1, marker), func(t *testing.T, path string) {
			if err := os.WriteFile(path+".1", []byte("older still\n"), 0o600); err != nil {
				t.Fatalf("seeding the rotated log: %v", err)
			}
		})
		lg.Logger.Info("fresh")

		if head := readHead(t, path+".1", len(marker)); head != marker {
			t.Errorf("rasp.log.1 starts with %q, want the log Init rotated", head)
		}
	})
}

// sized seeds a log file of exactly n bytes, opening with head. The tail is a
// hole rather than n bytes of writing, which keeps a 10 MB fixture instant.
func sized(n int, head string) func(*testing.T, string) {
	return func(t *testing.T, path string) {
		t.Helper()
		file, err := os.Create(path)
		if err != nil {
			t.Fatalf("seeding %s: %v", path, err)
		}
		defer file.Close()
		if _, err := file.WriteString(head); err != nil {
			t.Fatalf("seeding %s: %v", path, err)
		}
		if err := file.Truncate(int64(n)); err != nil {
			t.Fatalf("sizing %s: %v", path, err)
		}
	}
}

func size(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

func readHead(t *testing.T, path string, n int) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer file.Close()

	head := make([]byte, n)
	if _, err := io.ReadFull(file, head); err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(head)
}

// blockedDir returns a directory path that cannot be created, because a regular
// file stands where its parent would go. Permissions would do it too, but not
// for the root user CI sometimes runs as.
func blockedDir(t *testing.T) string {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", parent, err)
	}
	return filepath.Join(parent, "logs")
}

// capture redirects the process's stdout and stderr for the duration of fn.
func capture(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	dir := t.TempDir()
	outFile, errFile := create(t, filepath.Join(dir, "stdout")), create(t, filepath.Join(dir, "stderr"))
	defer outFile.Close()
	defer errFile.Close()

	realOut, realErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outFile, errFile
	defer func() { os.Stdout, os.Stderr = realOut, realErr }()

	fn()
	return read(t, outFile.Name()), read(t, errFile.Name())
}

func create(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	return file
}

func assertSilent(t *testing.T, stdout, stderr string) {
	t.Helper()
	if stdout != "" {
		t.Errorf("logx wrote to stdout, which belongs to the UI: %q", stdout)
	}
	if stderr != "" {
		t.Errorf("logx wrote to stderr: %q", stderr)
	}
}
