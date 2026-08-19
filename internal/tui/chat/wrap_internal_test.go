package chat

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestWrapBreaksLinesAtTheWidthItIsGiven. Wrapping draws what the user typed
// and what a tool call is doing — everything the markdown renderer is not asked
// to touch.
func TestWrapBreaksLinesAtTheWidthItIsGiven(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		width int
		want  string
	}{
		{"a width nobody has reported yet breaks nothing", "one two three four", 0, "one two three four"},
		{"breaking at the last space that fits", "one two three four", 8, "one two\nthree\nfour"},
		{"a word longer than the width breaks inside it", "supercalifragilistic", 5, "super\ncalif\nragil\nistic"},
		{"the line breaks already there are kept", "one\ntwo", 10, "one\ntwo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := wrap(tc.text, tc.width); got != tc.want {
				t.Errorf("%q at width %d draws\n%q\nwant\n%q", tc.text, tc.width, got, tc.want)
			}
		})
	}
}

// TestWrappingKeepsEveryCharacterAndFitsTheWidth is the property the table
// above cannot cover case by case. The wrapping is hand-rolled, and hand-rolled
// wrapping is where an off-by-one leaves a line one column too wide or eats the
// character it broke on.
func TestWrappingKeepsEveryCharacterAndFitsTheWidth(t *testing.T) {
	texts := []string{
		"", " ", "a", "aa aa", strings.Repeat("x", 50),
		"one two three four five six seven",
		"   three leading spaces and a tail long enough to break somewhere",
		"three trailing spaces   ",
		"multi\nline\ntext, the last line of which is long enough to wrap",
		"héllo wörld with accénts, two bytes each and one rune each",
	}
	for _, text := range texts {
		for width := 1; width <= 12; width++ {
			got := wrap(text, width)
			for _, line := range strings.Split(got, "\n") {
				if n := utf8.RuneCountInString(line); n > width {
					t.Errorf("%q at width %d drew a line of %d runes: %q", text, width, n, line)
				}
			}
			if want, have := squeeze(text), squeeze(got); want != have {
				t.Errorf("%q at width %d came back as %q, which is not the same text", text, width, got)
			}
		}
	}
}

// squeeze drops every run of whitespace. What survives it is what wrapping is
// not allowed to change.
func squeeze(s string) string { return strings.Join(strings.Fields(s), "") }
