// Package logx configures log/slog to write JSON to a file under the data
// directory, at a level the environment can set.
//
// Does not contain: anything bound for stdout — stdout belongs to the UI, and a
// stray log line corrupts the display. No secret may reach a log record, which
// is why the credential key list lives here and not behind an import of
// internal/config.
//
// That list is read against attribute keys, not values: a credential nested
// inside a map or a struct logged under a harmless key is still the call site's
// to keep out.
package logx
