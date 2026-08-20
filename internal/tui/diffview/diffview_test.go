package diffview_test

import (
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/tui/diffview"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// wide is a terminal nothing under test is cut at, so a line can be compared to
// the text it is meant to be.
const wide = 200

// sample is what go-udiff writes for a one-line replacement, header pair and
// all, with the tabs a Go file really carries.
var sample = lines(
	"--- a/auth.go",
	"+++ b/auth.go",
	"@@ -3,4 +3,4 @@",
	" func parse(r *http.Request) (Claims, error) {",
	"-\treturn decode(r.Header.Get(\"Authorization\"))",
	"+\treturn decode(r.Context().Value(headerKey).(string))",
	" }",
)

// TestEachLineClassIsDrawnInItsOwnToken is the whole of what a diff view is
// for: a reader tells an inserted line from a deleted one at a glance, and from
// the context it sits in. Checked against the palette's own tokens rather than
// merely against each other, because four distinct colours in the wrong order
// is a diff that paints its additions red.
func TestEachLineClassIsDrawnInItsOwnToken(t *testing.T) {
	p := styles.For(styles.Dark)
	drawn := strings.Split(diffview.Render(sample, wide, p), "\n")
	if len(drawn) != 5 {
		t.Fatalf("the diff drew %d lines:\n%s", len(drawn), strings.Join(drawn, "\n"))
	}

	for _, tc := range []struct {
		class string
		row   string
		token interface{ Render(...string) string }
	}{
		{"hunk header", drawn[0], p.DiffHunk},
		{"context", drawn[1], p.DiffContext},
		{"deleted", drawn[2], p.DiffRemoved},
		{"inserted", drawn[3], p.DiffAdded},
		{"closing context", drawn[4], p.DiffContext},
	} {
		got, want := colour(tc.row), colour(tc.token.Render("x"))
		switch {
		case want == "":
			t.Fatalf("the %s token draws nothing, so every comparison here holds vacuously", tc.class)
		case got != want:
			t.Errorf("the %s line is drawn %q, and its token is %q: %q", tc.class, got, want, text(tc.row))
		}
	}
}

// TestTheFileHeaderIsNotDrawn. go-udiff writes `--- a/x` and `+++ b/x` above
// the first hunk; the card this is drawn under already names the file, and two
// lines of it is a third of what a small diff has to say at 80 columns.
func TestTheFileHeaderIsNotDrawn(t *testing.T) {
	drawn := text(diffview.Render(sample, wide, styles.For(styles.Dark)))

	for _, header := range []string{"--- a/auth.go", "+++ b/auth.go"} {
		if strings.Contains(drawn, header) {
			t.Errorf("the diff draws %q:\n%s", header, drawn)
		}
	}
	if !strings.Contains(drawn, "@@ -3,4 +3,4 @@") {
		t.Errorf("the hunk header went with it:\n%s", drawn)
	}
}

// TestAWideLineIsCutRatherThanWrapped is the acceptance criterion stated as its
// consequence. A diff line broken over two rows reads as two changed lines, so
// every line has to stay inside the terminal on its own — and the cut has to be
// visible, or a reader takes a truncated line for the whole of it.
func TestAWideLineIsCutRatherThanWrapped(t *testing.T) {
	const width = 40

	drawn := diffview.Render(sample, width, styles.For(styles.Dark))
	rows := strings.Split(drawn, "\n")
	if want := 5; len(rows) != want {
		t.Fatalf("the diff drew %d rows for %d lines, so something wrapped:\n%s", len(rows), want, text(drawn))
	}
	for _, row := range rows {
		if n := lipgloss.Width(row); n > width {
			t.Errorf("a row runs %d columns into a terminal %d wide, and the terminal will wrap it: %q",
				n, width, text(row))
		}
	}
	if !strings.Contains(text(drawn), "…") {
		t.Errorf("no row says it was cut, so a reader takes a shortened line for the whole of it:\n%s", text(drawn))
	}
}

// TestAFilesOwnControlCharactersAreNotHandedToTheTerminal. A diff draws the
// bytes of a file nobody vetted. An ESC in one lets its contents recolour the
// screen or swallow the text after it — and a cut landing inside a sequence
// emits a half-written one, which is worse. A BEL sounds on every frame the
// line is in. A CR writes what comes next back over the line just drawn, and
// every line of every CRLF file ends with one.
func TestAFilesOwnControlCharactersAreNotHandedToTheTerminal(t *testing.T) {
	hostile := lines(
		"@@ -1,3 +1,3 @@",
		"-old line\r",
		"+colour \x1b[41mRED\x1b[0m, a bell \a and a null \x00, then enough text to reach the cut",
		// C1, which UTF-8 hides in plain sight: U+009B is CSI on its own, so a
		// terminal reading 8-bit controls obeys it exactly where dropping ESC was
		// meant to stop, and U+0085 is a line break that splits one row into two.
		"+eight-bit 31m and a next-line  after it",
	)

	drawn := diffview.Render(hostile, 40, styles.For(styles.Dark))
	for _, row := range strings.Split(drawn, "\n") {
		for _, escape := range []string{"\x1b[41m", "\x1b[0m"} {
			if strings.Contains(row, escape) {
				t.Errorf("the row hands the terminal %q, which came out of the file: %q", escape, row)
			}
		}
		if strings.ContainsAny(row, "\r\a\x00") {
			t.Errorf("the row carries a control character the terminal acts on: %q", row)
		}
		for _, r := range row {
			// Our own styling is the only escape allowed through, and it is the
			// one rune this range does not cover after the ESC.
			if r != 0x1b && r >= 0x7f && r <= 0x9f {
				t.Errorf("the row carries U+%04X, which a terminal reading eight-bit controls "+
					"obeys rather than draws: %q", r, row)
			}
		}
	}

	// Dropping the ESC and nothing else, so what it introduced stays visible as
	// text rather than vanishing from a line that says it changed.
	if !strings.Contains(text(drawn), "[41mRED") {
		t.Errorf("the escape's own text is gone as well, so the line no longer says what it holds:\n%s",
			text(drawn))
	}
}

// TestACutNeverLandsWiderThanTheTerminal is fit's cluster measurement across
// the shapes that break a per-rune one, at every width they can be cut at.
func TestACutNeverLandsWiderThanTheTerminal(t *testing.T) {
	for name, content := range map[string]string{
		"flags":               strings.Repeat("\U0001F1FA\U0001F1F8", 6),
		"variation selectors": strings.Repeat("☀️", 6),
		"combining marks":     strings.Repeat("é", 12),
		"wide":                strings.Repeat("漢", 12),
	} {
		t.Run(name, func(t *testing.T) {
			diff := lines("@@ -1,1 +1,1 @@", "+ab"+content)
			for width := 2; width <= 20; width++ {
				for _, row := range strings.Split(diffview.Render(diff, width, styles.For(styles.Dark)), "\n") {
					if n := lipgloss.Width(row); n > width {
						t.Errorf("at width %d a row measures %d: %q", width, n, text(row))
					}
				}
			}
		})
	}
}

// TestABidiOverrideCannotReorderWhatTheCardShows. The Trojan Source trick, and
// it is aimed at exactly this view: a right-to-left override makes a terminal
// draw a line in an order the bytes are not in, so the card can show a guard
// clause the file does not contain — in the one place a reader checks a change
// before accepting it. They measure as no cells, so nothing else here sees
// them, and rasp is not a security boundary: what this owes is that the diff it
// draws is the bytes it was given, in their order.
func TestABidiOverrideCannotReorderWhatTheCardShows(t *testing.T) {
	// Every invisible directional control, in one line each.
	for _, r := range []rune{0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069} {
		t.Run(strconv.FormatInt(int64(r), 16), func(t *testing.T) {
			trojan := lines("@@ -1,1 +1,1 @@", "+if (access) {"+string(r)+" // } "+string(r)+" nothing")

			drawn := diffview.Render(trojan, wide, styles.For(styles.Dark))
			if strings.ContainsRune(drawn, r) {
				t.Errorf("U+%04X reached the terminal, which reorders the row around it: %q", r, drawn)
			}
		})
	}
}

// TestALineThatFitsIsNotMarkedAsCut is the other half of measuring by cluster.
// Summing rune widths over-counts a flag emoji — two cells together, two cells
// each apart — so a line well inside the terminal was cut short and handed the
// mark that says the terminal did it, which is a renderer lying about the file.
func TestALineThatFitsIsNotMarkedAsCut(t *testing.T) {
	// Nineteen cells: three of text and sixteen of emoji.
	fits := lines("@@ -1,1 +1,1 @@", "+ab"+strings.Repeat("\U0001F1FA\U0001F1F8", 8))

	drawn := diffview.Render(fits, 30, styles.For(styles.Dark))
	if strings.Contains(text(drawn), "…") {
		t.Errorf("a 19-cell line in a 30-column terminal is marked as cut:\n%s", text(drawn))
	}
	if n := strings.Count(text(drawn), "\U0001F1FA"); n != 8 {
		t.Errorf("the line drew %d flags of 8, so it was shortened as well as mismeasured", n)
	}
}

// TestTabsAreExpandedBeforeAnythingIsMeasured. Cell measurement counts a tab as
// nothing, so a tab-indented line — which most Go is — measures short, is left
// uncut, and then wraps in the terminal: the failure the cut exists to prevent,
// arriving through the check meant to catch it.
func TestTabsAreExpandedBeforeAnythingIsMeasured(t *testing.T) {
	// One tab and a line of content, against a width the content alone fits and
	// the indented line does not.
	indented := lines("@@ -1,1 +1,1 @@", "+\t"+strings.Repeat("x", 20))
	const width = 22

	drawn := diffview.Render(indented, width, styles.For(styles.Dark))
	added := strings.Split(drawn, "\n")[1]

	if n := lipgloss.Width(added); n > width {
		t.Errorf("the indented line runs %d columns into a terminal %d wide: %q", n, width, text(added))
	}
	if strings.Contains(text(added), "\t") {
		t.Errorf("the line still carries a tab, whose width the terminal alone decides: %q", text(added))
	}
}

// TestATabAfterAClusterLandsOnTheRightStop. The stop is counted in cells, and a
// cluster is not its runes: a flag emoji is two cells drawn and four summed, so
// a tab after one lands three columns off if the column is added up per rune —
// and every later tab on the line inherits the drift, so the indentation the
// card shows is not the file's.
func TestATabAfterAClusterLandsOnTheRightStop(t *testing.T) {
	// `+` is column 0 and the flag covers 1 and 2, so the tab fills column 3
	// alone and `value` starts at the stop.
	diff := lines("@@ -1,1 +1,1 @@", "+\U0001F1FA\U0001F1F8\tvalue")

	drawn := text(diffview.Render(diff, wide, styles.For(styles.Dark)))
	added := strings.Split(drawn, "\n")[1]

	if want := "+\U0001F1FA\U0001F1F8 value"; added != want {
		t.Errorf("the line draws %q, want %q — the tab was placed by counting runes", added, want)
	}
}

// TestATerminalThatHasNotReportedItsSizeCutsNothing. Width arrives as a message
// after the first frames are drawn, and a zero read as a width would leave every
// line of every diff as one elision mark.
func TestATerminalThatHasNotReportedItsSizeCutsNothing(t *testing.T) {
	// The tab is still expanded — that is how the line reads, not how it is
	// measured — so the whole of it is the marker, three spaces and the rest.
	const whole = "+   return decode(r.Context().Value(headerKey).(string))"

	for _, width := range []int{0, -1} {
		drawn := text(diffview.Render(sample, width, styles.For(styles.Dark)))
		if !strings.Contains(drawn, whole) {
			t.Errorf("at width %d the diff is cut anyway:\n%s", width, drawn)
		}
	}
}

// TestADiffWithNothingInItDrawsNothing. go-udiff renders a change that turned
// out to change nothing as no text at all, and a card asking for its body back
// must get an empty string rather than a blank line to open onto.
func TestADiffWithNothingInItDrawsNothing(t *testing.T) {
	for name, unified := range map[string]string{"empty": "", "a newline": "\n"} {
		if got := diffview.Render(unified, wide, styles.For(styles.Dark)); got != "" {
			t.Errorf("%s drew %q", name, got)
		}
	}
}

// TestTheSameDiffIsDrawnDifferentlyOnEachBackground, which is the palette
// reaching the renderer at all: a view that built its own colours would draw
// the identical bytes whatever it was handed.
func TestTheSameDiffIsDrawnDifferentlyOnEachBackground(t *testing.T) {
	onDark := diffview.Render(sample, wide, styles.For(styles.Dark))
	onLight := diffview.Render(sample, wide, styles.For(styles.Light))

	if onDark == onLight {
		t.Fatal("the two palettes drew the same bytes")
	}
	if text(onDark) != text(onLight) {
		t.Errorf("the two palettes drew different text, not merely different colours:\n%s\n\n%s",
			text(onDark), text(onLight))
	}
}

// lines joins diff lines the way a unified diff arrives: newline-separated, and
// ending in one.
func lines(l ...string) string { return strings.Join(l, "\n") + "\n" }

// colour is the first escape sequence a row opens with, and empty for a row
// with none.
func colour(row string) string {
	i := strings.IndexByte(row, 0x1b)
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(row[i:], 'm')
	if j < 0 {
		return ""
	}
	return row[i : i+j+1]
}

// text is a rendered diff reduced to what it says. ansi.Strip rather than a
// scan to the next `m`, which swallows the rest of a row whenever a sequence
// is not the SGR one it assumes.
func text(drawn string) string { return ansi.Strip(drawn) }
