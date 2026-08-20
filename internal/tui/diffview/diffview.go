package diffview

import (
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/tui/styles"
)

const (
	elision = "…"

	// Four, not the terminal's eight: a diff is already two columns in, and eight
	// puts a doubly-indented Go line past 80 columns on whitespace alone.
	tabWidth = 4
)

// Render draws a unified diff, one screen line per line of it, in p's colours.
// Nothing wraps: a changed line broken across two rows reads as two changed
// lines, so a line past width is cut and marked instead, the whole of it left
// in the tool's Details for a horizontal scroll that has no binding yet. A
// width of zero or less is a terminal that has not reported its size, and
// nothing is cut.
func Render(unified string, width int, p styles.Palette) string {
	lines := body(unified)
	if len(lines) == 0 {
		return ""
	}

	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(draw(line, width, p))
	}
	return b.String()
}

// Draws reports that unified has lines a reader would see. Exported so that
// asking and drawing cannot disagree about a diff with only a header in it.
func Draws(unified string) bool { return len(body(unified)) > 0 }

// body drops the `--- a/x` and `+++ b/x` pair, which the card's own line
// already says. Positional rather than by prefix: a deleted line reading `-- x`
// is a deletion, and it can only be one below a hunk header.
func body(unified string) []string {
	unified = strings.TrimSuffix(unified, "\n")
	if unified == "" {
		return nil
	}
	lines := strings.Split(unified, "\n")
	if len(lines) >= 2 && strings.HasPrefix(lines[0], "--- ") && strings.HasPrefix(lines[1], "+++ ") {
		return lines[2:]
	}
	return lines
}

func draw(line string, width int, p styles.Palette) string {
	text, cut := fit(expand(line), width)
	drawn := classify(line, p).Render(text)
	if cut {
		drawn += p.Muted.Render(elision)
	}
	return drawn
}

// classify reads a line's class off the character the format puts it in. `\` is
// the one that is not source: "\ No newline at end of file" is the format
// talking about the file rather than a line out of it.
func classify(line string, p styles.Palette) lipgloss.Style {
	switch {
	case strings.HasPrefix(line, "@@"):
		return p.DiffHunk
	case strings.HasPrefix(line, "+"):
		return p.DiffAdded
	case strings.HasPrefix(line, "-"):
		return p.DiffRemoved
	case strings.HasPrefix(line, `\`):
		return p.Muted
	}
	return p.DiffContext
}

// expand turns a diff line into something a terminal draws rather than acts on:
// tabs become spaces to the next stop, every other control character goes.
//
// One problem, since a file is arbitrary bytes and this draws them. Each of
// these is a byte a terminal acts on instead of drawing, and none is visible in
// the diff it corrupts:
//
//   - a tab measures as no cells, so a tab-indented line — most Go — measures
//     short, survives the cut and wraps anyway;
//   - CR writes what follows back over the line, and ends every line of a CRLF
//     file; BEL sounds on every frame; ESC recolours the screen or swallows the
//     rest of the row;
//   - C1 does the last of those in one byte, U+009B being CSI and U+0085 a line
//     break, past where dropping ESC stops;
//   - a bidi override reorders the row, so the card can show a guard clause the
//     file does not contain — Trojan Source, aimed squarely at the view a reader
//     checks a change in.
//
// Dropping ESC alone leaves the rest of its sequence as literal text, which is
// visible and inert.
// Walked by grapheme cluster, which is what the column has to count: a flag
// emoji is two cells drawn and four summed per rune, so a tab after one would
// otherwise land at a stop the file does not have — and every later tab on the
// line inherits the drift. Counted as it goes rather than re-measured at each
// tab, or a line of a minified file pays a scan of itself per tab.
func expand(line string) string {
	var (
		b   strings.Builder
		col int
	)
	for rest := line; rest != ""; {
		cluster, w := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		rest = rest[len(cluster):]

		r, size := utf8.DecodeRuneInString(cluster)
		switch {
		case r == '\t':
			n := tabWidth - col%tabWidth
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case size == len(cluster) && (r < 0x20 || (r >= 0x7f && r <= 0x9f) || bidi(r)):
		default:
			b.WriteString(cluster)
			col += w
		}
	}
	return b.String()
}

// bidi reports the invisible directional formatting characters: the embeddings
// and overrides, the isolates that replaced them, and the three marks — LRM,
// RLM and the Arabic letter mark — which reorder a neutral run just as well.
// Rust's text_direction_codepoint_in_literal lint covers exactly this set.
func bidi(r rune) bool {
	switch r {
	case 0x061c, 0x200e, 0x200f:
		return true
	}
	return (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

// fit cuts line to width, keeping a column back for the mark, and reports
// whether it had to.
//
// By grapheme cluster, not by rune: a flag emoji is two cells together and two
// cells *each* apart, a sun plus its variation selector one rune of width and
// two cells drawn. Per-rune therefore calls a line too wide when it fits — and
// too narrow when it does not, which is the wrap this exists to prevent.
func fit(line string, width int) (string, bool) {
	if width <= 0 || ansi.StringWidth(line) <= width {
		return line, false
	}
	return ansi.Truncate(line, width-ansi.StringWidth(elision), ""), true
}
