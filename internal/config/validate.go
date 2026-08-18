package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// InvalidError is a setting rasp will not start with. It names where the value
// came from, because "which of my four config sources said that" is the only
// question the reader has.
type InvalidError struct {
	Origin Origin
	Key    string
	Reason string
}

func (e *InvalidError) Error() string {
	return fmt.Sprintf("%s: %s: %s", e.Origin, e.Key, e.Reason)
}

// validate checks one layer's contribution before it is merged. Per layer rather
// than on the merged result because both of its rules are about *where* a value
// was written: a project file setting yolo is refused even if a flag would have
// overridden it, since the next run without that flag is the one that gets it.
func validate(t tree, origin Origin) ([]Warning, error) {
	var warnings []Warning

	// A mode that is not a string is left to the shape check after the merge,
	// which names the same thing with the same origin.
	if raw, ok := lookupPath(t, "mode"); ok {
		if mode, ok := raw.(string); ok {
			if err := checkMode(mode, origin); err != nil {
				return nil, err
			}
		}
	}

	// Not an error, because ignoring it costs the user nothing — and not silence
	// either, because an override nothing evaluates still looks like a constraint.
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

const keyMaxOutputTokens = "max_output_tokens"

// checkResolved applies the rules that are about a value rather than about the
// layer that wrote it. Once, on the merged tree, so a cap a higher layer has
// already replaced is not refused on behalf of a run that never uses it.
//
// Nothing here checks a cap against the model's own ceiling: no model id is
// resolved against a catalog, so a cap the model will not take is sent as written
// and the API refuses it — the same answer the effort ladder gives.
func checkResolved(t tree, origins Origins) error {
	val, ok := lookupPath(t, keyMaxOutputTokens)
	if !ok {
		// Not reachable from a config file, where a null leaves the default
		// standing: this is the default itself gone missing, whose quiet
		// alternative is a zero cap refused once per run by an adapter.
		return &InvalidError{
			Origin: Origin{Layer: LayerDefault},
			Key:    keyMaxOutputTokens,
			Reason: "no reply cap resolved, and the built-in defaults are where one always comes from",
		}
	}

	// A value of the wrong sort, or one too large for an int, is inspect's to
	// report and it has already run.
	num, ok := val.(json.Number)
	if !ok {
		return nil
	}
	tokens, err := num.Int64()
	if err != nil || tokens > 0 {
		return nil
	}

	origin, _ := origins.At(keyMaxOutputTokens)
	return &InvalidError{
		Origin: origin,
		Key:    keyMaxOutputTokens,
		Reason: fmt.Sprintf("%d leaves no room for a reply; the cap is a number of tokens, "+
			"and a request asking for none is refused by the API", tokens),
	}
}

// checkMode applies both of design §10's rules about `mode`: the name has to
// exist, and yolo may be selected only where doing so is a deliberate act.
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
		// The environment and flags are the user's own act, but yolo arms a
		// bypass ahead of the permission ladder rather than selecting a preset
		// within it (design §10) — so it arrives through --yolo, not through
		// the precedence chain.
		reason += "Arm it explicitly with --yolo at launch, or /yolo in session."
	}

	return &InvalidError{Origin: origin, Key: "mode", Reason: reason}
}

func modeList() string { return strings.Join(modeNames, ", ") }
