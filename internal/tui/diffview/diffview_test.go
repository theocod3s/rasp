package diffview_test

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

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
// merely against each other, because three distinct colours in the wrong order
// is a diff that paints its additions red.
func TestEachLineClassIsDrawnInItsOwnToken(t *testing.T) {
	p := styles.For(styles.Dark)
	drawn := strings.Split(diffview.Render(sample, wide, p), "\n")
	if len(drawn) != 4 {
		t.Fatalf("the diff drew %d lines:\n%s", len(drawn), strings.Join(drawn, "\n"))
	}

	for _, tc := range []struct {
		class string
		row   string
		token interface{ Render(...string) string }
	}{
		{"context", drawn[0], p.DiffContext},
		{"deleted", drawn[1], p.DiffRemoved},
		{"inserted", drawn[2], p.DiffAdded},
		{"closing context", drawn[3], p.DiffContext},
	} {
		// The second colour on the row, the first being the gutter's: a row opens
		// with its line number, and comparing that to a line class would hold for
		// every class at once.
		seqs, want := colours(tc.row), colours(tc.token.Render("x"))
		switch {
		case len(want) != 1:
			t.Fatalf("the %s token draws %d colours, so the comparison below is not one", tc.class, len(want))
		case len(seqs) != 2:
			t.Fatalf("the %s row is drawn in %d colours, want the gutter's and its own: %q",
				tc.class, len(seqs), text(tc.row))
		case seqs[1] != want[0]:
			t.Errorf("the %s line is drawn %q, and its token is %q: %q",
				tc.class, seqs[1], want[0], text(tc.row))
		}
	}
}

// TestNoHeaderLineIsDrawn. go-udiff writes `--- a/x` and `+++ b/x` above the
// first hunk; the card this is drawn under already names the file, and two
// lines of it is a third of what a small diff has to say at 80 columns. The
// `@@` header goes for a different reason: it is arithmetic, and the gutter
// under it is the answer the arithmetic was for.
func TestNoHeaderLineIsDrawn(t *testing.T) {
	drawn := text(diffview.Render(sample, wide, styles.For(styles.Dark)))

	for _, header := range []string{"--- a/auth.go", "+++ b/auth.go", "@@ -3,4 +3,4 @@", "@@"} {
		if strings.Contains(drawn, header) {
			t.Errorf("the diff draws %q:\n%s", header, drawn)
		}
	}
}

// TestTheGutterNumbersMatchTheHunkArithmetic is the whole of this view's claim:
// the number beside a line is the line it is in the file, so a reader can open
// the file at it. Every number comes off a `@@` header and the sides count
// separately — a deletion is numbered in the old file, an addition and a
// context line in the new — so an off-by-one is invisible in the rendering and
// only visible against the arithmetic, which is why the whole row is written
// out here rather than merely spot-checked.
//
// Written as the rows a reader sees, so the alignment is asserted with the
// numbers: right-aligned under the widest one in the diff, which is what keeps
// the units column in one place down the card.
func TestTheGutterNumbersMatchTheHunkArithmetic(t *testing.T) {
	for name, tc := range map[string]struct{ unified, want string }{
		// The first line of a file, which is where an off-by-one shows plainly:
		// numbering from the header's start rather than from one past it.
		"a hunk starting at line 1": {
			unified: lines("--- a/x", "+++ b/x", "@@ -1,3 +1,4 @@", " a", "-b", "+B", "+C", " c"),
			want:    "1  a\n2 -b\n2 +B\n3 +C\n4  c",
		},
		// Two hunks: the second is numbered from its own header rather than
		// carried on from where the first ran out, and the jump between them is
		// what the boundary mark stands for.
		"a second hunk starts where its header says": {
			unified: lines("@@ -1,2 +1,2 @@", "-a", "+A", "@@ -10,3 +10,3 @@", " x", "-y", "+Y"),
			want:    " 1 -a\n 1 +A\n ⋯\n10  x\n11 -y\n11 +Y",
		},
		// Three, so the mark is between each pair and not merely after the first.
		"every hunk after the first is marked": {
			unified: lines("@@ -1,1 +1,1 @@", " a", "@@ -20,1 +20,1 @@", " b", "@@ -300,1 +300,1 @@", " c"),
			want:    "  1  a\n  ⋯\n 20  b\n  ⋯\n300  c",
		},
		// Nothing on the new side at all, so every number is the old file's and a
		// renderer numbering from the new one would count 4, 4, 4.
		"a deletion-only hunk": {
			unified: lines("@@ -5,3 +4,0 @@", "-a", "-b", "-c"),
			want:    "5 -a\n6 -b\n7 -c",
		},
		// A new file, whose old side is the empty range `-0,0`: the numbers are
		// the new file's and start at 1.
		"a creation numbers from one": {
			unified: lines("--- a/x", "+++ b/x", "@@ -0,0 +1,3 @@", "+x", "+y", "+z"),
			want:    "1 +x\n2 +y\n3 +z",
		},
		"an insertion into an existing file": {
			unified: lines("@@ -3,0 +4,2 @@", "+p", "+q"),
			want:    "4 +p\n5 +q",
		},
		// The format leaves a count of one out, so a parse that required the comma
		// would number nothing here.
		"a header with its counts left out": {
			unified: lines("@@ -7 +7 @@", "-a", "+A"),
			want:    "7 -a\n7 +A",
		},
		// The one line that is not source. It belongs to the line above it and is
		// in neither file, so it takes no number and consumes none.
		"a file that ends without a newline": {
			unified: lines("@@ -1,1 +1,1 @@", "-a", `\ No newline at end of file`, "+a"),
			want:    "1 -a\n  \\ No newline at end of file\n1 +a",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := text(diffview.Render(tc.unified, wide, styles.For(styles.Dark))); got != tc.want {
				t.Errorf("the diff draws\n\n%s\n\nwant\n\n%s", got, tc.want)
			}
		})
	}
}

// TestNothingIsNumberedWithoutAHeaderToNumberItFrom. The numbers come off the
// `@@` headers and from nowhere else — there is no file here to count lines in
// — so a body arriving without a header it can read gets no gutter at all,
// rather than one starting at 1. A wrong line number is the one kind of wrong
// this view can be without looking it.
func TestNothingIsNumberedWithoutAHeaderToNumberItFrom(t *testing.T) {
	for name, unified := range map[string]string{
		"no header at all":              lines(" a", "-b", "+c"),
		"a header that does not parse":  lines("@@ nonsense @@", " a", "-b", "+c"),
		"a header cut short":            lines("@@ -1,3", " a", "-b", "+c"),
		"a header with no second side":  lines("@@ -1,3 @@", " a", "-b", "+c"),
		"a header whose start is not a": lines("@@ -x,3 +x,3 @@", " a", "-b", "+c"),
	} {
		t.Run(name, func(t *testing.T) {
			if got, want := text(diffview.Render(unified, wide, styles.For(styles.Dark))), " a\n-b\n+c"; got != want {
				t.Errorf("the diff draws %q, want %q — a number was invented for a line "+
					"nothing places", got, want)
			}
		})
	}
}

// TestAHunkBoundaryIsDrawnWhereItsHeaderWas. `@@ -12,5 +12,6 @@` is arithmetic
// nobody does, and the gutter under it is the answer — but one thing in it is
// not in the numbers: that the file between two hunks is missing. Nobody reads
// a card by noticing that 15 is followed by 40, so the header goes and a mark
// stays, in the token that was the header's.
func TestAHunkBoundaryIsDrawnWhereItsHeaderWas(t *testing.T) {
	p := styles.For(styles.Dark)
	rows := strings.Split(diffview.Render(lines(
		"@@ -1,1 +1,1 @@", " a", "@@ -40,1 +40,1 @@", " b"), wide, p), "\n")
	if len(rows) != 3 {
		t.Fatalf("the diff drew %d rows, want a line, a boundary and a line:\n%s",
			len(rows), text(strings.Join(rows, "\n")))
	}

	hunk := colours(p.DiffHunk.Render("x"))
	if len(hunk) != 1 {
		t.Fatalf("the DiffHunk token draws %d colours, so the comparison below is not one", len(hunk))
	}
	if got := colours(rows[1]); len(got) != 1 || got[0] != hunk[0] {
		t.Errorf("the boundary is drawn %v, and the hunk token is %q", got, hunk[0])
	}

	// And nothing above the first hunk: a card's own line sits directly over it,
	// so a mark there says a stretch was skipped where none was.
	if strings.Contains(text(rows[0]), "⋯") {
		t.Errorf("the diff opens on a boundary mark: %q", text(rows[0]))
	}
	if drawn := text(diffview.Render(sample, wide, p)); strings.Contains(drawn, "⋯") {
		t.Errorf("a single-hunk diff draws a boundary with nothing on either side of it:\n%s", drawn)
	}
}

// TestTheGutterIsMutedOnBothBackgrounds. The numbers are the UI's own writing
// rather than the file's, so they are drawn a step back from the code beside
// them — and taken from the palette, which is the only thing that follows the
// terminal a session is actually running on.
func TestTheGutterIsMutedOnBothBackgrounds(t *testing.T) {
	for name, bg := range map[string]styles.Background{"dark": styles.Dark, "light": styles.Light} {
		t.Run(name, func(t *testing.T) {
			p := styles.For(bg)
			muted := colours(p.Muted.Render("x"))
			if len(muted) != 1 {
				t.Fatalf("the Muted token draws %d colours, so the comparison below is not one", len(muted))
			}

			rows := strings.Split(diffview.Render(sample, wide, p), "\n")
			if len(rows) == 0 {
				t.Fatal("the diff drew no rows, so nothing here is checked")
			}
			for _, row := range rows {
				got := colours(row)
				if len(got) < 2 {
					t.Fatalf("the row is drawn in %d colours, so it carries no gutter to check: %q",
						len(got), text(row))
				}
				if got[0] != muted[0] {
					t.Errorf("the gutter draws %q and Muted is %q: %q", got[0], muted[0], text(row))
				}
			}
		})
	}
}

// TestTheGutterComesOutOfTheContentsWidth. The numbers are columns the UI spent
// rather than columns the terminal gained: a renderer that cut the line to the
// whole width and then set a gutter in front of it would push every long line
// one gutter past the edge, where it wraps — which is the single thing the cut
// exists to stop, arriving through the change meant to make the card readable.
//
// Asserted as arithmetic rather than as "the row fits", since a row cut twice
// fits too, and the reader would silently be shown less of the file than the
// terminal had room for.
func TestTheGutterComesOutOfTheContentsWidth(t *testing.T) {
	const width = 40

	for name, tc := range map[string]struct {
		unified string
		gutter  int
	}{
		"one digit":    {lines("@@ -1,1 +1,1 @@", "+"+strings.Repeat("x", 100)), 2},
		"three digits": {lines("@@ -120,3 +120,3 @@", "+"+strings.Repeat("x", 100), " y", " z"), 4},
	} {
		t.Run(name, func(t *testing.T) {
			row := strings.Split(text(diffview.Render(tc.unified, width, styles.For(styles.Dark))), "\n")[0]

			if got := ansi.StringWidth(row); got != width {
				t.Fatalf("the row measures %d columns in a terminal %d wide: %q", got, width, row)
			}
			// Two columns of the line's own share are not file: the `+` the format
			// puts every added line in, and the mark saying the line was cut.
			if got, want := strings.Count(row, "x"), width-tc.gutter-2; got != want {
				t.Errorf("the line drew %d columns of the file, and the terminal has %d once the "+
					"%d-column gutter, the marker and the cut mark are out of it: %q",
					got, want, tc.gutter, row)
			}
		})
	}
}

// TestATerminalTooNarrowForBothKeepsTheCode. The gutter comes out of the line's
// width, so on a terminal narrow enough there is nothing to take it from: a row
// of numbers and a cut mark says nothing about the change, and one column
// further the row is wider than the terminal and wraps.
func TestATerminalTooNarrowForBothKeepsTheCode(t *testing.T) {
	// Numbered in the hundreds, so the gutter is four columns and the widths it
	// does not fit into are ordinary rather than absurd.
	unified := lines("@@ -120,1 +120,1 @@", "+"+strings.Repeat("x", 40))

	for width := 1; width <= 4; width++ {
		// The `+` marker and the cut mark, neither of them file. At a width of one
		// there is room for neither and the mark alone is the row.
		file := max(0, width-2)

		row := text(diffview.Render(unified, width, styles.For(styles.Dark)))
		switch {
		case ansi.StringWidth(row) > width:
			t.Errorf("at width %d the row measures %d, so the terminal wraps it: %q",
				width, ansi.StringWidth(row), row)
		case strings.ContainsAny(row, "0123456789"):
			t.Errorf("at width %d the row is still numbered, and there is no room for both: %q", width, row)
		case strings.Count(row, "x") != file:
			t.Errorf("at width %d the line drew %d columns of the %d the dropped gutter left it: %q",
				width, strings.Count(row, "x"), file, row)
		}
	}

	// And at the first width that holds a gutter and a column of content, it is
	// back — so the rule above is a floor rather than the gutter never drawing.
	if row := text(diffview.Render(unified, 5, styles.For(styles.Dark))); !strings.HasPrefix(row, "120 ") {
		t.Errorf("at the first width that fits one, there is no gutter: %q", row)
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
	if want := 4; len(rows) != want {
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
// the shapes that break a per-rune one, at every width they can be cut at. Two
// hunks, so the boundary mark is measured at those widths as well: it is set
// beside the gutter rather than cut to fit, and it is the one glyph drawn here
// whose width the Unicode tables call ambiguous.
func TestACutNeverLandsWiderThanTheTerminal(t *testing.T) {
	for name, content := range map[string]string{
		"flags":               strings.Repeat("\U0001F1FA\U0001F1F8", 6),
		"variation selectors": strings.Repeat("☀️", 6),
		"combining marks":     strings.Repeat("é", 12),
		"wide":                strings.Repeat("漢", 12),
	} {
		t.Run(name, func(t *testing.T) {
			diff := lines("@@ -1,1 +1,1 @@", "+ab"+content, "@@ -90,1 +90,1 @@", "+ab"+content)
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

// TestBytesThatAreNotTextAreNotHandedToTheTerminal. A file is bytes, not
// necessarily UTF-8, and the checks that drop control characters decode a rune
// before they look — so a byte that is not a rune reaches the default arm and
// goes out as it arrived. The one that matters is a lone 0x9b, which is CSI in
// a terminal reading eight-bit controls: the same escape the C1 range exists to
// stop, arriving by the one route that skips the range.
func TestBytesThatAreNotTextAreNotHandedToTheTerminal(t *testing.T) {
	for name, content := range map[string]string{
		"a lone eight-bit CSI":     "+before\x9bafter",
		"a truncated rune":         "+before\xe2\x82after",
		"a stray continuation":     "+before\xbfafter",
		"an overlong encoding":     "+before\xc0\xafafter",
		"a byte no encoding holds": "+before\xffafter",
	} {
		t.Run(name, func(t *testing.T) {
			drawn := diffview.Render(lines("@@ -1,1 +1,1 @@", content), wide, styles.For(styles.Dark))

			if !utf8.ValidString(drawn) {
				t.Errorf("the row is not text: %q", drawn)
			}
			for _, b := range []byte(drawn) {
				if b >= 0x80 && b <= 0x9f {
					t.Errorf("the row carries the raw byte %#x, which a terminal reading eight-bit "+
						"controls obeys: %q", b, drawn)
				}
			}
			// The line still says what surrounded the byte, so a reader is not
			// silently shown less of the file than it holds.
			if got := text(drawn); !strings.Contains(got, "before") || !strings.Contains(got, "after") {
				t.Errorf("the row lost the text around the byte: %q", got)
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
	// Written out from the threat rather than read off the implementation, so
	// the test cannot become a copy of whatever the renderer happens to check —
	// which is the shape that can never find a gap. The nine explicit controls
	// are the Trojan Source set; the three marks reorder a neutral run by a
	// weaker mechanism, to the same end.
	//
	//	LRM RLM ALM, the marks; LRE RLE PDF LRO RLO, the embeddings and
	//	overrides; LRI RLI FSI PDI, the isolates that replaced them.
	for _, r := range []rune{
		0x200e, 0x200f, 0x061c,
		0x202a, 0x202b, 0x202c, 0x202d, 0x202e,
		0x2066, 0x2067, 0x2068, 0x2069,
	} {
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
	added := strings.Split(drawn, "\n")[0]

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
// card shows is not the file's. The gutter is in each expectation and counts
// for none of it: the column is the line's own, so the file's indentation
// survives a column of numbers being set in front of it.
func TestATabAfterAClusterLandsOnTheRightStop(t *testing.T) {
	for name, tc := range map[string]struct{ content, want string }{
		// Two runes and two cells: `+` is column 0, the flag covers 1 and 2, so
		// the tab fills column 3 alone.
		"a flag, wider than its runes": {
			content: "+\U0001F1FA\U0001F1F8\tvalue",
			want:    "1 +\U0001F1FA\U0001F1F8 value",
		},
		// Two runes and one cell, which is the case a rune count gets wrong in
		// the other direction: e plus a combining acute sits in column 1 alone,
		// so the tab has columns 2 and 3 to fill.
		"a combining mark, narrower than its runes": {
			content: "+é\tvalue",
			want:    "1 +é  value",
		},
	} {
		t.Run(name, func(t *testing.T) {
			drawn := text(diffview.Render(lines("@@ -1,1 +1,1 @@", tc.content), wide, styles.For(styles.Dark)))
			if added := strings.Split(drawn, "\n")[0]; added != tc.want {
				t.Errorf("the line draws %q, want %q — the tab was placed by counting runes",
					added, tc.want)
			}
		})
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

// colours is the escape sequences a row opens a run of text with, in order and
// resets left out. A numbered row carries two — the gutter's and the line's own
// — so which of them a test means has to be said rather than assumed.
func colours(row string) []string {
	var found []string
	for rest := row; ; {
		i := strings.IndexByte(rest, 0x1b)
		if i < 0 {
			return found
		}
		j := strings.IndexByte(rest[i:], 'm')
		if j < 0 {
			return found
		}
		if seq := rest[i : i+j+1]; seq != "\x1b[m" && seq != "\x1b[0m" {
			found = append(found, seq)
		}
		rest = rest[i+j+1:]
	}
}

// text is a rendered diff reduced to what it says. ansi.Strip rather than a
// scan to the next `m`, which swallows the rest of a row whenever a sequence
// is not the SGR one it assumes.
func text(drawn string) string { return ansi.Strip(drawn) }
