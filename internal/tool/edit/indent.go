package edit

import "strings"

func leadingWhitespace(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

// detectIndentUnit reads an indentation convention out of text: a tab, or the N
// spaces that most often separate one level from the next.
//
// It answers "" for text that shows none, rather than defaulting to four spaces.
// A default would be an invention, and the caller has a second body of text —
// old_string and new_string are written in the model's own convention, the file
// in its own — so the honest fallback is to borrow the other side's unit and
// leave the indentation as it stands.
func detectIndentUnit(sets ...[]line) string {
	tabs, spaces := 0, 0
	steps := map[int]int{}

	for _, set := range sets {
		// Levels are read off the change from one line to the next, so a file
		// indented four deep votes for 4 three times rather than for 4, 8 and 12.
		width := 0
		for _, l := range set {
			if strings.TrimSpace(l.text) == "" {
				continue
			}
			indent := leadingWhitespace(l.text)
			if strings.ContainsRune(indent, '\t') {
				tabs++
				width = 0
				continue
			}
			if indent != "" {
				spaces++
			}
			if len(indent) > width {
				steps[len(indent)-width]++
			}
			width = len(indent)
		}
	}

	switch {
	case tabs > spaces:
		return "\t"
	case spaces == 0:
		return ""
	}

	// Map order is randomised, so the tie-break has to be explicit or the unit
	// detected for one file changes between runs.
	best, votes := 0, 0
	for step, n := range steps {
		if n > votes || (n == votes && step < best) {
			best, votes = step, n
		}
	}
	if best == 0 {
		return ""
	}
	return strings.Repeat(" ", best)
}

// indentDepth splits an indent into whole units and the padding left over, which
// is usually what aligns a continuation line under an open paren and has to
// survive re-indentation as it stands.
func indentDepth(indent, unit string) (int, string) {
	if unit == "" {
		return 0, indent
	}
	depth := 0
	for strings.HasPrefix(indent, unit) {
		indent = indent[len(unit):]
		depth++
	}
	return depth, indent
}

// reindent re-lays replacement at the indentation matched holds, which is the
// file's own. Each line keeps the depth it had relative to the line of old it
// answers to, and that depth is re-emitted in the file's unit, so a four-space
// block spliced into a tab-indented file arrives as tabs.
//
// Lines at or below their reference line take its indentation verbatim rather
// than fileUnit repeated, so an odd but deliberate alignment at the match site
// survives an edit that did not set out to change it.
func reindent(replacement string, oldLines, matched []line, fileUnit, modelUnit string) string {
	newLines := splitLines(replacement)

	var b strings.Builder
	for i, l := range newLines {
		body := strings.TrimLeft(l.text, " \t")
		if body != "" {
			// A replacement standing line for line against old_string is the common
			// edit, and the only shape where a line has a line of its own to take
			// its indentation from. Anything else hangs off the first, which is all
			// the correspondence there is.
			ref := 0
			if len(newLines) == len(oldLines) {
				ref = i
			}
			fileBase := leadingWhitespace(matched[ref].text)
			oldDepth, oldPad := indentDepth(leadingWhitespace(oldLines[ref].text), modelUnit)
			depth, pad := indentDepth(leadingWhitespace(l.text), modelUnit)
			if strings.HasPrefix(pad, oldPad) {
				pad = pad[len(oldPad):]
			}

			if rel := depth - oldDepth; rel >= 0 {
				b.WriteString(fileBase)
				b.WriteString(strings.Repeat(fileUnit, rel))
			} else {
				baseDepth, _ := indentDepth(fileBase, fileUnit)
				b.WriteString(strings.Repeat(fileUnit, max(0, baseDepth+rel)))
			}
			b.WriteString(pad)
			b.WriteString(body)
		}
		b.WriteString(l.term)
	}
	return b.String()
}
