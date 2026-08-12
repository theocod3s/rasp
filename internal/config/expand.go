package config

import (
	"fmt"
	"strings"
)

// A value in a config file may reference the environment or a command instead
// of holding a secret literally. design §10 defines five forms:
//
//	sk-ant-…            a literal
//	$VAR   ${VAR}       an environment variable
//	${VAR:-default}     that variable, or the default when unset or empty
//	${VAR:?msg}         that variable, or refuse to start and print msg
//	$(command)          run it, take the trimmed stdout
//
// Parsing is separated from resolving so that this half is a pure function
// over a string, with no environment and no subprocess — which is what makes
// it fuzzable, the same argument internal/tool/edit makes for the match ladder.

// segKind is what one piece of a parsed value is.
type segKind int

const (
	segLiteral segKind = iota
	segVar
	segCommand
)

// segment is one piece of a value: text to keep as it stands, a variable to
// look up, or a command to run.
type segment struct {
	kind segKind

	// text is the literal, the variable name, or the command.
	text string

	// op is the operator for a variable: 0 for a bare reference, '-' for a
	// default, '?' for a message. arg carries whichever followed.
	op  byte
	arg string
}

// Escaping a literal dollar. Config values are JSON strings, where `\$` is not
// a legal escape, so the doubling has to happen inside the value — which is
// docker-compose's rule for the same reason.
const dollarEscape = "$$"

// parseValue splits a config value into its literal and substituted pieces.
//
// A value with no `$` in it is the overwhelmingly common case and comes back as
// a single literal segment.
func parseValue(s string) ([]segment, error) {
	var segs []segment
	emitLiteral := func(text string) {
		if text == "" {
			return
		}
		// Runs of literal text merge, so `$$x` is one segment rather than two.
		if n := len(segs); n > 0 && segs[n-1].kind == segLiteral {
			segs[n-1].text += text
			return
		}
		segs = append(segs, segment{kind: segLiteral, text: text})
	}

	for i := 0; i < len(s); {
		dollar := strings.IndexByte(s[i:], '$')
		if dollar < 0 {
			emitLiteral(s[i:])
			break
		}
		emitLiteral(s[i : i+dollar])
		i += dollar

		if i+1 >= len(s) {
			return nil, fmt.Errorf("a lone %q at the end of the value; write %q for a literal dollar",
				"$", dollarEscape)
		}

		switch s[i+1] {
		case '$':
			emitLiteral("$")
			i += 2

		case '(':
			cmd, n, err := parseCommand(s[i:])
			if err != nil {
				return nil, err
			}
			segs = append(segs, segment{kind: segCommand, text: cmd})
			i += n

		case '{':
			seg, n, err := parseBraced(s[i:])
			if err != nil {
				return nil, err
			}
			segs = append(segs, seg)
			i += n

		default:
			name := leadingName(s[i+1:])
			if name == "" {
				return nil, fmt.Errorf("%q is not a variable reference; write %q for a literal dollar",
					s[i:i+2], dollarEscape)
			}
			segs = append(segs, segment{kind: segVar, text: name})
			i += 1 + len(name)
		}
	}

	return segs, nil
}

// parseCommand reads `$(command)` from the front of s, returning the command
// and how many bytes it consumed.
//
// Parentheses nest, and a parenthesis inside a quoted string is not a
// parenthesis — `$(echo ")")` is one command, not a truncated one. That is the
// same mistake a naive JSONC comment stripper makes with a `//` inside a URL,
// and it is worth not making twice.
func parseCommand(s string) (cmd string, n int, err error) {
	depth := 0
	var quote byte

	for i := 1; i < len(s); i++ { // s[0] is '$', s[1] is '('
		c := s[i]
		if quote != 0 {
			switch {
			case c == '\\' && quote == '"' && i+1 < len(s):
				i++ // an escaped character inside double quotes
			case c == quote:
				quote = 0
			}
			continue
		}

		switch c {
		case '\'', '"':
			quote = c
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return s[2:i], i + 1, nil
			}
		}
	}

	return "", 0, fmt.Errorf("unterminated %q — no closing parenthesis", "$(")
}

// parseBraced reads `${…}` from the front of s.
func parseBraced(s string) (seg segment, n int, err error) {
	end := strings.IndexByte(s, '}')
	if end < 0 {
		return segment{}, 0, fmt.Errorf("unterminated %q — no closing brace", "${")
	}
	body := s[2:end]

	name, rest, hasOp := strings.Cut(body, ":")
	if name == "" || leadingName(name) != name {
		return segment{}, 0, fmt.Errorf("%q is not a valid variable name", name)
	}
	if !hasOp {
		return segment{kind: segVar, text: name}, end + 1, nil
	}
	if rest == "" {
		return segment{}, 0, fmt.Errorf("${%s:} has no operator; want %q or %q", name, ":-", ":?")
	}

	switch op := rest[0]; op {
	case '-', '?':
		return segment{kind: segVar, text: name, op: op, arg: rest[1:]}, end + 1, nil

	case '=', '+':
		// Deliberately unsupported (design §10), and rejected rather than
		// treated as a literal: assigning back into our own environment and
		// substituting-when-set are answers to questions nobody asks about a
		// credential, and silently passing `${VAR:=x}` through as text would
		// surface much later as an API key that is nine characters of
		// punctuation.
		return segment{}, 0, fmt.Errorf("${%s:%c…} is not supported; want %q or %q",
			name, op, ":-", ":?")

	default:
		return segment{}, 0, fmt.Errorf("${%s:%c…} is not an operator; want %q or %q",
			name, op, ":-", ":?")
	}
}

// leadingName returns the environment-variable name at the front of s, or ""
// if it does not start with one.
func leadingName(s string) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9' && i > 0: // a name may not start with a digit
		default:
			return s[:i]
		}
	}
	return s
}

// needsExpansion reports whether a value has anything to substitute.
//
// It is an optimisation and nothing more: parseValue returns a dollar-free
// value as a single literal segment, so skipping it changes no behaviour, only
// the allocation. Almost every config value is a literal, which is what makes
// the check worth its line.
func needsExpansion(value string) bool {
	return strings.ContainsRune(value, '$')
}
