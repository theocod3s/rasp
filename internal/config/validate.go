package config

import (
	"fmt"
	"slices"
	"strings"
)

// InvalidError is a setting rasp will not start with. It names where the value
// came from, because "which of my four config sources said that" is the only
// question the reader actually has.
type InvalidError struct {
	Origin Origin
	Key    string
	Reason string
}

func (e *InvalidError) Error() string {
	return fmt.Sprintf("%s: %s: %s", e.Origin, e.Key, e.Reason)
}

// validate checks one layer's contribution before it is merged, returning the
// problems worth warning about and an error for the ones that stop startup.
//
// It runs per layer rather than on the merged result because both of its rules
// are about *where* a value was written, not what it resolved to. A project
// file setting yolo has to be refused even if a flag would have overridden it:
// the file is still asking, and the next run without that flag is the one that
// gets it.
func validate(t tree, origin Origin) ([]Warning, error) {
	var warnings []Warning

	if raw, ok := lookupPath(t, "mode"); ok {
		mode, ok := raw.(string)
		if !ok {
			return nil, &InvalidError{
				Origin: origin,
				Key:    "mode",
				Reason: fmt.Sprintf("want one of %s, got %s", modeList(), describeJSON(raw)),
			}
		}
		if err := checkMode(mode, origin); err != nil {
			return nil, err
		}
	}

	// A yolo preset override is not an error, because ignoring it costs the
	// user nothing. It is not silence either: an override that looks like a
	// constraint and is never evaluated is worse than no override at all.
	if _, ok := lookupPath(t, "modes", ModeYolo); ok {
		warnings = append(warnings, Warning{
			Origin: origin,
			Key:    joinPath([]string{"modes", ModeYolo}),
			Message: "ignored — yolo skips permission checks entirely rather than " +
				"consulting a pattern, so an override here would only look like a constraint",
		})
	}

	return warnings, nil
}

// checkMode applies both of design §10's rules about `mode`: the name has to
// exist, and yolo may be selected only where selecting it is a deliberate act.
func checkMode(mode string, origin Origin) error {
	if !slices.Contains(modeNames, mode) {
		return &InvalidError{
			Origin: origin,
			Key:    "mode",
			Reason: fmt.Sprintf("unknown mode %q; want one of %s", mode, modeList()),
		}
	}
	if mode != ModeYolo || origin.Layer == LayerGlobal {
		return nil
	}

	reason := "yolo turns off every approval prompt, so it is not a value any " +
		"layer can set on your behalf. "
	switch origin.Layer {
	case LayerProject:
		reason += "A project config travels with the repository: honouring this " +
			"would disable the guardrails on `git clone`, before anyone had read a " +
			"line of the code. Set it in the global config instead, or arm it for a " +
			"single run with --yolo."
	default:
		// The environment and the flags are the user's own act, but yolo still
		// arrives through --yolo rather than through the precedence chain: it
		// arms a bypass ahead of the permission ladder rather than selecting a
		// preset within it (design §10), and one name for one mechanism is
		// what keeps that distinction legible.
		reason += "Arm it explicitly with --yolo at launch, or /yolo in session."
	}

	return &InvalidError{Origin: origin, Key: "mode", Reason: reason}
}

// modeList renders the accepted mode names for an error message.
func modeList() string { return strings.Join(modeNames, ", ") }

// describeJSON names the kind of a decoded JSON value, for an error that has
// to say what arrived instead of what was wanted.
func describeJSON(val any) string {
	switch val.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case string:
		return "a string"
	case []any:
		return "an array"
	case tree:
		return "an object"
	default:
		// json.Number, and anything a future decoder produces.
		return "a number"
	}
}
