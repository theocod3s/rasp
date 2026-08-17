// Package logx configures log/slog to write JSON to a file under the data
// directory, at a level the environment can set. Init also takes over the
// process's standard sinks — Go's log package and the slog default — so a
// dependency writing to either lands in the file.
//
// Does not contain: anything bound for stdout — stdout belongs to the UI, and a
// stray log line corrupts the display. No secret may reach a log record, which
// is why the credential key list lives here and not behind an import of
// internal/config.
//
// That list is read against each attribute's own key, never its value and never
// the group enclosing it: a credential inside a map, a struct, or an attribute
// named only by the group above it is still the call site's to keep out.
package logx
