package edit

import (
	"errors"
	"fmt"
	"strings"
)

// Rung identifies which rung of the ladder placed a replacement. Two of the four
// in prd §6.2 place text at all, so these values name those two rather than
// numbering the ladder.
type Rung int

const (
	// Exact is old appearing in src byte for byte. A caller telling the model its
	// match was not byte-exact should compare against this rather than list the
	// others, so a rung added later reaches the model as the news it is.
	Exact Rung = iota + 1

	// Normalized is old matching whole lines of src once the whitespace at their
	// ends is ignored, with the replacement re-indented to the file.
	Normalized
)

// Replacement is what a successful pass down the ladder produced.
type Replacement struct {
	Text  string // src with the replacement applied
	Count int    // occurrences replaced
	Rung  Rung
}

var (
	// ErrEmpty reports that there is no text to match. An empty old sits at every
	// byte boundary in src, so a ladder accepting it would be choosing a position
	// rather than finding one.
	ErrEmpty = errors.New("the text to replace is empty")

	// ErrUnchanged reports that the edit would leave src exactly as it stands and
	// still report an edit. new being old is one way in; the other is a normalized
	// match whose replacement re-indents back to what the file already holds.
	ErrUnchanged = errors.New("the replacement would leave the text exactly as it is")

	// ErrNotFound reports that no rung matched. Every miss carries it, including
	// the ones that found somewhere to point at, so a caller that only wants to
	// know whether the ladder failed can ask with errors.Is.
	ErrNotFound = errors.New("no rung of the ladder matched the text to replace")
)

// AmbiguousError reports that old matched more than once and the caller did not
// ask for every occurrence. It guards both matching rungs: normalizing whitespace
// widens what counts as an occurrence and so can only make ambiguity likelier.
type AmbiguousError struct{ Count int }

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("the text to replace matched %d times", e.Count)
}

// NotFoundError is the ladder's last rung: nothing matched, and this is where the
// file came closest, so the model can read what is actually there instead of
// guessing a second time at what it got wrong.
type NotFoundError struct {
	// Line is the 1-based line the closest run starts on, or 0 when no line of
	// the file equalled any line of old and there is nothing honest to point at.
	Line int

	// Actual is the file's own content there, one numbered line each, with its
	// whitespace shown as TabGlyph and SpaceGlyph. Empty whenever Line is 0.
	Actual string
}

func (e *NotFoundError) Error() string {
	if e.Line == 0 {
		return ErrNotFound.Error()
	}
	return fmt.Sprintf("%s; the file comes closest at line %d", ErrNotFound.Error(), e.Line)
}

func (e *NotFoundError) Unwrap() error { return ErrNotFound }

// Apply replaces old with new in src, taking its parameters in the order
// strings.Replace does.
//
// The ladder is byte-exact match, ambiguity refusal, whitespace-normalized match
// with re-indentation, and a diagnostic. More than one occurrence without
// replaceAll is an *AmbiguousError at whichever rung found them, and never a
// replacement of the first: the model reproduced text it read and has no way to
// know which copy it landed on, so choosing one silently corrupts a file, while
// asking for more surrounding context costs a turn.
//
// Nothing below compares characters approximately, and that is a boundary rather
// than a gap: both the normalized rung and the diagnostic work in whole equal
// lines, because approximate matching is how an edit lands confidently in the
// wrong place.
func Apply(src, old, new string, replaceAll bool) (Replacement, error) {
	switch {
	case old == "":
		return Replacement{}, ErrEmpty
	case old == new:
		return Replacement{}, ErrUnchanged
	}

	if n := strings.Count(src, old); n > 0 {
		if n > 1 && !replaceAll {
			return Replacement{}, &AmbiguousError{Count: n}
		}
		return Replacement{Text: strings.Replace(src, old, new, n), Count: n, Rung: Exact}, nil
	}

	srcLines, oldLines := splitLines(src), splitLines(old)
	srcTrim, oldTrim := trimLines(srcLines), trimLines(oldLines)
	if !anchored(oldTrim) {
		return Replacement{}, &NotFoundError{}
	}

	starts := alignedMatches(srcTrim, oldTrim)
	switch {
	case len(starts) == 0:
		return Replacement{}, nearMiss(srcLines, srcTrim, oldTrim)
	case len(starts) > 1 && !replaceAll:
		return Replacement{}, &AmbiguousError{Count: len(starts)}
	}

	modelUnit := detectIndentUnit(oldLines, splitLines(new))
	fileUnit := detectIndentUnit(srcLines)
	if fileUnit == "" {
		fileUnit = modelUnit
	}

	// A replacement that re-indents to what the file already holds differs from
	// old in whitespace alone, so the edit it reported would be a diff of nothing.
	text := spliceAligned(srcLines, oldLines, starts, new, fileUnit, modelUnit)
	if text == src {
		return Replacement{}, ErrUnchanged
	}
	return Replacement{Text: text, Count: len(starts), Rung: Normalized}, nil
}
