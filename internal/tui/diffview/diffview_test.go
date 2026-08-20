package diffview_test

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

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

// text is a rendered diff reduced to what it says.
func text(drawn string) string {
	var b strings.Builder
	for i := 0; i < len(drawn); i++ {
		if drawn[i] == 0x1b {
			for i < len(drawn) && drawn[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(drawn[i])
	}
	return b.String()
}
