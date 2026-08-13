package config

// This test is in package config rather than config_test because its subject
// is parseValue itself. Design §13 fuzzes internal/tool/edit for the reason
// that applies here too: a pure string function with no I/O is the one shape
// where fuzzing is cheap, and the failure it hunts — a parse that succeeds
// while meaning something else — is one no table test would think to write.

import (
	"slices"
	"strings"
	"testing"
)

// FuzzParseValue hunts for a panic, for a literal silently rewritten, and for
// a parse that is not stable.
//
// The stability check is the interesting one, and it is deliberately expressed
// without a second copy of the grammar: render the segments back into source
// and parse *that*, and the segments must come out identical. An earlier
// version of this test compared against a hand-written canonical form, which
// only proved that two implementations of the same parser disagreed — the `$`
// in `$(echo $$)` is shell syntax passed through untouched, not our escape,
// and the canonicaliser did not know that.
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
			// A rejected value is a fine outcome. It only has to be a
			// rejection rather than a panic, which getting here proves.
			return
		}

		// The common path, checked exactly: a value with nothing to substitute
		// comes back as itself. Any drift here is a literal credential quietly
		// rewritten, which would fail authentication somewhere far from this
		// package with nothing pointing back.
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

// render writes segments back out as source.
func render(segs []segment) string {
	var out strings.Builder
	for _, seg := range segs {
		switch seg.kind {
		case segLiteral:
			// A literal dollar has to go back out escaped, or rendering a
			// literal would produce a reference on the way back in.
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
