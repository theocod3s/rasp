// Package logx configures log/slog to write JSON to a file under the data
// directory, at a level the environment can set.
//
// Does not contain: anything bound for stdout — stdout belongs to the UI, and a
// stray log line corrupts the display. No secret may ever reach a log record,
// which is why the credential key list lives here and not behind an import of
// internal/config.
package logx
