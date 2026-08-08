// Package logx configures log/slog to write to a file under the state
// directory, at a level the environment can set.
//
// Does not contain: anything bound for stdout — stdout belongs to the UI, and a
// stray log line corrupts the display. No secret may ever reach a log record.
package logx
