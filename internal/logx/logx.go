package logx

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

const (
	// levelVar and fileVar are environment-only, with no config key behind
	// them: a logging setting that lives in the config file is unreachable
	// exactly when the thing being debugged is config loading.
	levelVar = "RASP_LOG_LEVEL"
	fileVar  = "RASP_LOG_FILE"

	dataHomeVar = "XDG_DATA_HOME"

	// maxBytes is the size above which Init rotates. Two bounded files is the
	// whole policy — the alternative is a 4 GB file discovered in six months.
	maxBytes = 10 << 20

	rotatedSuffix = ".1"
)

// Log is a configured logger and the file behind it.
type Log struct {
	Logger *slog.Logger

	// Path is the file being written, empty when logging is disabled.
	Path string

	// Warnings carry what went wrong, for the caller to show once at startup.
	Warnings []string

	file *os.File
}

// Init opens the log file and returns a logger writing JSON to it.
//
// It cannot fail: every problem degrades to a discarding logger and a warning
// for the caller to show, because losing logs beats refusing to start
// (design §12).
//
// getenv may be nil, which reads the process environment.
func Init(getenv func(string) (string, bool)) *Log {
	if getenv == nil {
		getenv = os.LookupEnv
	}
	lg := &Log{Logger: slog.New(slog.DiscardHandler)}

	level := lg.level(getenv)

	path, err := logPath(getenv)
	if err != nil {
		lg.warn("logging is off: %v", err)
		return lg
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		lg.warn("logging is off: %v", err)
		return lg
	}
	if err := rotate(path); err != nil {
		lg.warn("%s could not be rotated and will keep growing: %v", path, err)
	}
	// 0o600 because a log record holds prompts, tool output and file contents.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		lg.warn("logging is off: %v", err)
		return lg
	}

	lg.file = file
	lg.Path = path
	lg.Logger = slog.New(redacting{slog.NewJSONHandler(file, &slog.HandlerOptions{Level: level})})
	return lg
}

// Close closes the log file, including when logging is disabled.
func (l *Log) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	return file.Close()
}

func (l *Log) warn(format string, args ...any) {
	l.Warnings = append(l.Warnings, fmt.Sprintf(format, args...))
}

func (l *Log) level(getenv func(string) (string, bool)) slog.Level {
	raw, ok := getenv(levelVar)
	if !ok || raw == "" {
		return slog.LevelInfo
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		l.warn("%s=%q is not a level (debug, info, warn, error); using info", levelVar, raw)
		return slog.LevelInfo
	}
	return level
}

// logPath walks XDG by hand, as config does for its own file: the Go stdlib's
// per-user directory helpers answer ~/Library/... on macOS, which is not the
// path design §9 documents.
func logPath(getenv func(string) (string, bool)) (string, error) {
	if path, ok := getenv(fileVar); ok && path != "" {
		return path, nil
	}
	if dir, ok := getenv(dataHomeVar); ok && dir != "" {
		return filepath.Join(dir, "rasp", "logs", "rasp.log"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the log directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "rasp", "logs", "rasp.log"), nil
}

// rotate moves an oversized log aside so a fresh one starts. The previous
// rotation is overwritten rather than shifted along, which is what keeps the
// bound at two files without a rotation library.
func rotate(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() <= maxBytes {
		return nil
	}
	return os.Rename(path, path+rotatedSuffix)
}
