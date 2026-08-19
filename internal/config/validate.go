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

	if err := checkModeRules(t, origin); err != nil {
		return nil, err
	}

	return warnings, nil
}

// ruleKeys are the answers a mode override gives directly, and patternKeys the
// two it gives through a table of globs. Both mirror ModePermissions, and a
// field added there without a line here goes unchecked — which is what
// TestEveryRuleInAModeOverrideIsChecked walks the struct to catch.
var (
	ruleKeys    = []string{"read", "write", "edit", "fetch"}
	patternKeys = []string{"bash", "mcp"}
)

// checkModeRules refuses a permission rule rasp cannot read, before the layer
// that wrote it is merged away. permission.Compile refuses one as well, but by
// then the only thing left to name is the mode, and the answer to "which of my
// config files said that" is gone.
//
// An error rather than a warning, for the reason a misspelled mode is one: an
// override that loads clean and is never consulted reads to its author as a
// constraint that is being enforced.
func checkModeRules(t tree, origin Origin) error {
	for _, mode := range modeNames {
		// modes.yolo is dropped whole and warned about above; refusing to start
		// over a typo inside it would be refusing over something nobody reads.
		if mode == ModeYolo {
			continue
		}
		set, ok := lookupPath(t, "modes", mode)
		if !ok {
			continue
		}
		// A mode override that is not an object at all is left to the shape
		// check after the merge, which names it with this same origin.
		byKey, ok := set.(tree)
		if !ok {
			continue
		}

		for _, key := range ruleKeys {
			if err := checkRule(byKey[key], origin, "modes", mode, key); err != nil {
				return err
			}
		}
		for _, key := range patternKeys {
			patterns, ok := byKey[key].(tree)
			if !ok {
				continue
			}
			for _, pattern := range sortedKeys(patterns) {
				path := []string{"modes", mode, key, pattern}
				if pattern == "" {
					return &InvalidError{
						Origin: origin,
						Key:    joinPath(path),
						Reason: "the empty pattern matches an empty command and nothing else; " +
							`write "*" for a catch-all`,
					}
				}
				if err := checkRule(patterns[pattern], origin, path...); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkRule accepts a value that is not a string: what arrived instead is the
// shape check's to name, and it names it with the same origin.
func checkRule(val any, origin Origin, path ...string) error {
	rule, ok := val.(string)
	if !ok || slices.Contains(ruleNames, rule) {
		return nil
	}
	return &InvalidError{
		Origin: origin,
		Key:    joinPath(path),
		Reason: fmt.Sprintf("unknown permission rule %q; want one of %s",
			rule, strings.Join(ruleNames, ", ")),
	}
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
