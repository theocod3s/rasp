package config

import (
	"encoding/json"
	"fmt"
)

// Hidden stands in for a credential wherever a resolved value is printed.
const Hidden = "(hidden)"

// secretKeys are the key-path shapes whose values are credentials, the two
// design §10 sends through the secret resolver. "*" matches any single key.
var secretKeys = [][]string{
	{"providers", wildcardKey, "api_key"},
	{"mcp", "servers", wildcardKey, "env", wildcardKey},
}

// IsSecret reports whether a key path holds a credential. It hides
// unconditionally rather than showing values that only reference a secret:
// `$(op read …)` is safe to print, but deciding that needs the expansion grammar,
// and a second copy of it here is a worse bargain than a line of output nobody
// needed.
func IsSecret(key string) bool {
	segments := splitPath(key)
	for _, shape := range secretKeys {
		if matchesShape(segments, shape) {
			return true
		}
	}
	return false
}

func matchesShape(segments, shape []string) bool {
	if len(segments) != len(shape) {
		return false
	}
	for i, want := range shape {
		if want != wildcardKey && want != segments[i] {
			return false
		}
	}
	return true
}

// Display renders one resolved value for printing, with credentials hidden.
func Display(key string, val any) string {
	if IsSecret(key) {
		return Hidden
	}
	switch v := val.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case nil:
		return "null"
	}
	// Arrays and empty objects print as the JSON the user wrote, the form they
	// would edit.
	if encoded, err := json.Marshal(val); err == nil {
		return string(encoded)
	}
	return fmt.Sprint(val)
}
