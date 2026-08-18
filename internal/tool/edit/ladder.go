package edit

import (
	"errors"
	"fmt"
	"strings"
)

// Rung identifies which rung of the ladder placed a replacement (prd §6.2).
type Rung int

const (
	// Exact is rung 1: old appeared in src byte for byte, and the only rung
	// implemented. A caller telling the model its match was not byte-exact should
	// compare against this rather than assume, since the normalized rung below
	// takes the next value.
	Exact Rung = iota + 1
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

	// ErrUnchanged reports that new is old, which would rewrite src to itself and
	// report an edit.
	ErrUnchanged = errors.New("the replacement is identical to the text to replace")

	// ErrNotFound reports that no rung matched, which is where the normalized rungs
	// attach: an exact miss is the whole answer only while rung 1 is the only rung.
	ErrNotFound = errors.New("no rung of the ladder matched the text to replace")
)

// AmbiguousError reports rung 2: old matched more than once and the caller did
// not ask for every occurrence.
type AmbiguousError struct{ Count int }

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("the text to replace matched %d times", e.Count)
}

// Apply replaces old with new in src, taking its parameters in the order
// strings.Replace does.
//
// Rung 1 is a byte-exact match; rung 2 is the refusal that guards it. More than one
// occurrence without replaceAll is an *AmbiguousError and never a replacement of
// the first: the model reproduced text it read and has no way to know which copy
// it landed on, so choosing one silently corrupts a file, while asking for more
// surrounding context costs a turn.
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

	return Replacement{}, ErrNotFound
}
