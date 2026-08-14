package config

// In package config rather than config_test because its subject is parseValue
// itself. Design §13's argument for fuzzing internal/tool/edit applies here: a
// pure string function with no I/O is where fuzzing is cheap, and the failure it
// hunts — a parse that succeeds while meaning something else — is one no table
// test would think to write.

import (
	"slices"
	"strings"
	"testing"
)

// FuzzParseValue hunts for a panic, a literal silently rewritten, and a parse
// that is not stable.
//
// The stability check avoids a second copy of the grammar: render the segments
// back into source, parse *that*, and require identical segments. An earlier
// version compared against a hand-written canonical form, which only proved two
// implementations of the same parser disagreed — the `$` in `$(echo $$)` is
// shell syntax passed through untouched, not our escape.
func FuzzParseValue(f *testing.F) {
	seeds := []string{
		"",
		"sk-ant-literal",
		"$KEY",
		"${KEY}",
		"${KEY:-fallback}",
		"${KEY:?run 'op signin' first}",
		"$(op read op://vault/key)",
		`$(echo ")" done)`,
		"$(echo $$)",
		"Bearer ${KEY}!",
		"sk-a$$bc",
		"$",
		"${",
		"$(",
		"${}",
		"${KEY:=x}",
		"${KEY:+x}",
		"$(a$(b)c)",
		"$$$KEY",
		"${A:-${B}}",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, value string) {
		segs, err := parseValue(value)
		if err != nil {
			// A rejection is a fine outcome, and getting here proves it was
			// not a panic.
			return
		}

		// A value with nothing to substitute comes back as itself. Any drift is a
		// literal credential quietly rewritten, which fails authentication far
		// from here with nothing pointing back.
		if !strings.ContainsRune(value, '$') {
			switch {
			case value == "" && len(segs) == 0:
			case len(segs) == 1 && segs[0].kind == segLiteral && segs[0].text == value:
			default:
				t.Fatalf("parseValue(%q) = %+v, want the value unchanged", value, segs)
			}
			return
		}

		rebuilt := render(segs)
		again, err := parseValue(rebuilt)
		if err != nil {
			t.Fatalf("parseValue(%q) rendered %q, which no longer parses: %v", value, rebuilt, err)
		}
		if !slices.Equal(segs, again) {
			t.Errorf("parseValue is not stable\n  input:    %q\n  rendered: %q\n  first:    %+v\n  second:   %+v",
				value, rebuilt, segs, again)
		}
	})
}

func render(segs []segment) string {
	var out strings.Builder
	for _, seg := range segs {
		switch seg.kind {
		case segLiteral:
			// Escaped on the way out, or rendering a literal produces a
			// reference on the way back in.
			out.WriteString(strings.ReplaceAll(seg.text, "$", dollarEscape))
		case segCommand:
			out.WriteString("$(")
			out.WriteString(seg.text)
			out.WriteString(")")
		case segVar:
			out.WriteString("${")
			out.WriteString(seg.text)
			if seg.op != 0 {
				out.WriteByte(':')
				out.WriteByte(seg.op)
				out.WriteString(seg.arg)
			}
			out.WriteString("}")
		}
	}
	return out.String()
}
