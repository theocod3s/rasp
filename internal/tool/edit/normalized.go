package edit

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// TabGlyph and SpaceGlyph stand in for whitespace in NotFoundError.Actual. A
// caller quoting that text should say which is which: the model is being shown
// the bytes it could not see, so unexplained glyphs trade one invisible
// difference for another.
const (
	TabGlyph   = "→"
	SpaceGlyph = "·"
)

// maxNearMissLines bounds what a near miss quotes back. old_string can be
// hundreds of lines, and what the model needs is the shape of the location, not
// the file returned to it a second time.
const maxNearMissLines = 20

// line is a line of text and the terminator that followed it, so that splitting
// a file and rejoining it returns the file — \r\n and a missing final newline
// included.
type line struct{ text, term string }

func splitLines(s string) []line {
	var lines []line
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			return append(lines, line{text: s})
		}
		text, term := s[:i], "\n"
		if strings.HasSuffix(text, "\r") {
			text, term = text[:len(text)-1], "\r\n"
		}
		lines = append(lines, line{text: text, term: term})
		s = s[i+1:]
	}
	return lines
}

// trimLines is the whole of "whitespace normalized": every line loses what sits
// at its two ends, which is where the drift the ladder exists for lives — tabs
// against spaces, a depth the model re-typed, a trailing space nobody can see.
//
// Runs inside a line are deliberately left alone. Two spaces between tokens
// where the file has one is a difference in content, not in indentation, and
// accepting it would splice new_string over bytes the model never compared.
func trimLines(lines []line) []string {
	trimmed := make([]string, len(lines))
	for i, l := range lines {
		trimmed[i] = strings.TrimSpace(l.text)
	}
	return trimmed
}

// anchored reports whether old carries any content at all once trimmed. A block
// that normalizes to nothing sits at every blank line in the file, which is
// ErrEmpty's hazard wearing whitespace.
func anchored(oldTrim []string) bool {
	return slices.ContainsFunc(oldTrim, func(s string) bool { return s != "" })
}

// alignedMatches returns the first line index of every run of source lines equal
// to old line for line once trimmed, scanning left to right and never
// overlapping — the same non-overlapping count strings.Replace works to, so a
// run counted here is a run that will be replaced.
//
// Whole runs only. A match that began or ended mid-line would splice new_string
// into the middle of a line the model never looked at, which is the one outcome
// worse than refusing the edit.
func alignedMatches(srcTrim, oldTrim []string) []int {
	n := len(oldTrim)
	if n == 0 || n > len(srcTrim) {
		return nil
	}
	var starts []int
	for i := 0; i+n <= len(srcTrim); {
		if slices.Equal(srcTrim[i:i+n], oldTrim) {
			starts = append(starts, i)
			i += n
			continue
		}
		i++
	}
	return starts
}

// spliceAligned replaces each matched run with replacement, re-indented to that
// run's own position — two matches at two depths each come out at their own.
func spliceAligned(srcLines, oldLines []line, starts []int, replacement, fileUnit, modelUnit string) string {
	n := len(oldLines)
	// old ending mid-line means the terminator closing the matched run was never
	// part of the match, so it survives it — the same bytes strings.Replace would
	// have left in place.
	keepTerm := oldLines[n-1].term == ""

	var b strings.Builder
	next := 0
	for _, start := range starts {
		writeLines(&b, srcLines[next:start])
		b.WriteString(reindent(replacement, oldLines, srcLines[start:start+n], fileUnit, modelUnit))
		if keepTerm {
			b.WriteString(srcLines[start+n-1].term)
		}
		next = start + n
	}
	writeLines(&b, srcLines[next:])
	return b.String()
}

func writeLines(b *strings.Builder, lines []line) {
	for _, l := range lines {
		b.WriteString(l.text)
		b.WriteString(l.term)
	}
}

// nearMiss is rung 4: the file's own content where old_string came closest,
// scored by whole trimmed lines that are equal. Nothing here compares
// characters, and that is the boundary rather than an omission — a location
// found by edit distance is a plausible wrong place, and pointing the model at
// one is how a file gets corrupted by a confident retry.
//
// No line equal to any of old's leaves nothing to anchor on, and a window picked
// anyway would be the file's first lines dressed as evidence.
func nearMiss(srcLines []line, srcTrim, oldTrim []string) *NotFoundError {
	best, score := 0, 0
	for i := range srcTrim {
		matched := 0
		for j, want := range oldTrim {
			if i+j >= len(srcTrim) {
				break
			}
			if want != "" && srcTrim[i+j] == want {
				matched++
			}
		}
		if matched > score {
			best, score = i, matched
		}
	}
	if score == 0 {
		return &NotFoundError{}
	}

	end := min(best+len(oldTrim), len(srcLines), best+maxNearMissLines)
	return &NotFoundError{Line: best + 1, Actual: visualize(srcLines[best:end], best+1)}
}

func visualize(lines []line, first int) string {
	var b strings.Builder
	width := len(strconv.Itoa(first + len(lines) - 1))
	for i, l := range lines {
		fmt.Fprintf(&b, "%*d | %s\n", width, first+i, visualizeLine(l.text))
	}
	return b.String()
}

// visualizeLine spends a glyph where whitespace is invisible and carries the
// difference — the two ends of the line, plus every tab — and leaves the rest as
// it is, since a line whose every space is a dot is a line nobody can read.
func visualizeLine(s string) string {
	body := strings.TrimLeft(s, " \t")
	lead := s[:len(s)-len(body)]
	text := strings.TrimRight(body, " \t")
	return glyphs(lead) + strings.ReplaceAll(text, "\t", TabGlyph) + glyphs(body[len(text):])
}

func glyphs(whitespace string) string {
	var b strings.Builder
	for _, r := range whitespace {
		if r == '\t' {
			b.WriteString(TabGlyph)
		} else {
			b.WriteString(SpaceGlyph)
		}
	}
	return b.String()
}
