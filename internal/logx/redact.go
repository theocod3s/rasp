package logx

import (
	"context"
	"log/slog"
	"strings"
)

// Redacted stands in for a credential wherever an attribute carries one.
const Redacted = "(redacted)"

// credentialKeys are the attribute names whose values never reach the file.
// The list lives here rather than reusing config.IsSecret, which matches config
// key *paths* — and importing config would invert the layering (design §2).
//
// Keys are compared after lowercasing and dropping - and _, so api_key, apiKey
// and API-KEY are one entry.
var credentialKeys = []string{
	"apikey",
	"authorization",
	"xapikey",
	"token",
	"password",
}

// redacting replaces credential values on their way through a handler. A
// handler rather than a naming convention plus one test, because the call site
// that leaks a key is by definition the one nobody thought to write a test for.
type redacting struct{ slog.Handler }

func (h redacting) Handle(ctx context.Context, rec slog.Record) error {
	clean := slog.NewRecord(rec.Time, rec.Level, rec.Message, rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(redact(a))
		return true
	})
	return h.Handler.Handle(ctx, clean)
}

// WithAttrs and WithGroup are overridden because the embedded handler's own
// versions return the inner handler, quietly dropping the wrapper for every
// record logged through a derived logger.

func (h redacting) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = redact(a)
	}
	return redacting{h.Handler.WithAttrs(clean)}
}

func (h redacting) WithGroup(name string) slog.Handler {
	return redacting{h.Handler.WithGroup(name)}
}

func redact(a slog.Attr) slog.Attr {
	// Resolved first: a LogValuer decides its own value, including whether that
	// value turns out to be a group.
	a.Value = a.Value.Resolve()
	if isCredential(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	if a.Value.Kind() == slog.KindGroup {
		members := a.Value.Group()
		clean := make([]slog.Attr, len(members))
		for i, member := range members {
			clean[i] = redact(member)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(clean...)}
	}
	return a
}

// isCredential matches a key that ends in one of the names, so the qualified
// spellings people actually type are covered: anthropic_api_key, access_token.
// A key that merely contains one is not a match — input_tokens is a count, and
// a substring rule would hide the numbers most worth reading.
func isCredential(key string) bool {
	normal := keySeparators.Replace(strings.ToLower(key))
	for _, candidate := range credentialKeys {
		if strings.HasSuffix(normal, candidate) {
			return true
		}
	}
	return false
}

var keySeparators = strings.NewReplacer("-", "", "_", "")
