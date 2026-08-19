package permission

import (
	"strings"
	"unicode/utf8"
)

// glob is one pattern, split at compile time into the runs of fixed text
// between its stars. Two characters are special and no others: `*` matches any
// run of any characters, `?` matches exactly one, and everything else — `[`,
// `]` and `\` included — is literal.
//
// `*` crossing `/` is where this departs from filepath.Match, and the departure
// is load-bearing: patterns are matched against whole command lines, so
// `git diff*` has to cover `git diff internal/permission/service.go`. Under
// filepath.Match's separator rule it would not, and every allow in a mode's
// preset would quietly stop covering commands that name a path.
//
// Brackets and backslashes stay literal because a shell command carries
// `[ -f x ]` and `find . -exec {} \;` far more often than anyone writing a
// permission pattern wants a character class — and a class parsed out of one
// would match a single character instead of the text the user typed, which is a
// wrong answer rather than a loud one.
type glob struct {
	// chunks are the runs between stars, in order, each possibly holding `?`.
	chunks []string

	anchored bool // the pattern does not open with `*`, so chunks[0] sits at index 0
	openEnd  bool // the pattern ends with `*`, so anything may follow the last chunk
}

func parseGlob(p string) glob {
	g := glob{
		anchored: !strings.HasPrefix(p, "*"),
		openEnd:  strings.HasSuffix(p, "*"),
	}
	for _, chunk := range strings.Split(p, "*") {
		if chunk != "" {
			g.chunks = append(g.chunks, chunk)
		}
	}
	return g
}

func (g glob) match(s string) bool {
	if len(g.chunks) == 0 {
		return g.openEnd || s == ""
	}

	rest := s
	for i, chunk := range g.chunks {
		last := i == len(g.chunks)-1 && !g.openEnd

		switch {
		case i == 0 && g.anchored:
			n, ok := matchChunk(chunk, rest)
			if !ok || (last && n != len(rest)) {
				return false
			}
			rest = rest[n:]
		case last:
			return matchChunkAtEnd(chunk, rest)
		default:
			// The leftmost placement is safe to commit to: the star before this
			// chunk absorbs whatever precedes it, so a later placement can never
			// leave the chunks after it more room than the earliest one does.
			n, ok := searchChunk(chunk, rest)
			if !ok {
				return false
			}
			rest = rest[n:]
		}
	}
	return true
}

// matchChunk reports whether chunk matches a prefix of s, and how many bytes of
// s that took. Literals compare byte by byte so that two different malformed
// bytes cannot match each other, which is what decoding both to U+FFFD first
// would do; `?` stands for a whole character and so decodes one.
func matchChunk(chunk, s string) (int, bool) {
	i := 0
	for ; chunk != ""; chunk = chunk[1:] {
		if i >= len(s) {
			return 0, false
		}
		if chunk[0] == '?' {
			_, size := utf8.DecodeRuneInString(s[i:])
			i += size
			continue
		}
		if chunk[0] != s[i] {
			return 0, false
		}
		i++
	}
	return i, true
}

// searchChunk finds the leftmost placement of chunk in s and returns the byte
// index just past it.
func searchChunk(chunk, s string) (int, bool) {
	for i := range len(s) + 1 {
		if n, ok := matchChunk(chunk, s[i:]); ok {
			return i + n, true
		}
	}
	return 0, false
}

// matchChunkAtEnd reports whether chunk can be placed so that it finishes at the
// end of s. It is the rightmost placement that matters here, which is why the
// leftmost search above cannot answer this case.
func matchChunkAtEnd(chunk, s string) bool {
	for i := range len(s) + 1 {
		if n, ok := matchChunk(chunk, s[i:]); ok && i+n == len(s) {
			return true
		}
	}
	return false
}

// specificity is design §7.3's ordering key: the count of characters a pattern
// pins down, which is every character that is not a wildcard. `find *` scores 5
// and `find * -delete*` scores 13, so the second answers for `find . -delete`
// and the first answers for everything else find is handed.
func specificity(p string) int {
	n := 0
	for _, r := range p {
		if r != '*' && r != '?' {
			n++
		}
	}
	return n
}
