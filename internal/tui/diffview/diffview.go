package diffview

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/tui/styles"
)

const (
	elision = "…"

	// gap is what a hunk boundary draws as. The header it stands in for said one
	// thing the numbers do not — that the file between two hunks is missing,
	// which nobody reads a card by noticing 15 is followed by 40 — so the
	// arithmetic goes and the break stays.
	gap = "⋯"

	// Four, not the terminal's eight: a diff is already two columns in, and eight
	// puts a doubly-indented Go line past 80 columns on whitespace alone.
	tabWidth = 4
)

// Render draws a unified diff, one screen line per line of it, in p's colours
// and under a line-number gutter read off the `@@` headers. Nothing wraps: a
// changed line broken across two rows reads as two changed lines, so a line
// past width is cut and marked instead, the whole of it left in the tool's
// Details for a horizontal scroll that has no binding yet. A width of zero or
// less is a terminal that has not reported its size, and nothing is cut.
func Render(unified string, width int, p styles.Palette) string {
	rows := parse(unified)
	if len(rows) == 0 {
		return ""
	}
	g := measure(rows, width)

	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(g.draw(r, width, p))
	}
	return b.String()
}

// Draws reports that unified has lines a reader would see. Exported so that
// asking and drawing cannot disagree about a diff with only a header in it.
func Draws(unified string) bool { return len(parse(unified)) > 0 }

type class int

const (
	context class = iota
	added
	removed
	// note is `\ No newline at end of file`, the one line that is not source:
	// the format talking about the file rather than a line out of it.
	note
	boundary
)

// row is one drawn line: the number the gutter shows against it, zero for a
// line neither file numbers, and the diff line whole, marker byte and all.
type row struct {
	num   int
	text  string
	class class
}

// parse reads a unified diff into the rows a reader sees, numbered off the `@@`
// headers — the new file's line for an addition or a context line, the old
// file's for a deletion, so each side is numbered in the file it exists in. The
// headers are not rows themselves.
//
// A header that does not parse stops the numbering rather than carrying on from
// where it had got to: a gutter is worth its columns only while it is right,
// and a wrong number is not visible as wrong.
func parse(unified string) []row {
	unified = strings.TrimSuffix(unified, "\n")
	if unified == "" {
		return nil
	}
	lines := strings.Split(unified, "\n")

	// The `--- a/x` and `+++ b/x` pair goes, since the card's own line already
	// says the path. Positional rather than by prefix: a deleted line reading
	// `-- x` is a deletion, and it can only be one below a hunk header.
	if len(lines) >= 2 && strings.HasPrefix(lines[0], "--- ") && strings.HasPrefix(lines[1], "+++ ") {
		lines = lines[2:]
	}

	var (
		rows         []row
		oldNo, newNo int
		numbering    bool
		insideAHunk  bool
	)
	take := func(n *int) int {
		if !numbering {
			return 0
		}
		*n++
		return *n - 1
	}

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "@@"):
			if insideAHunk {
				rows = append(rows, row{class: boundary})
			}
			insideAHunk = true
			oldNo, newNo, numbering = hunk(line)
		case strings.HasPrefix(line, "+"):
			rows = append(rows, row{num: take(&newNo), text: line, class: added})
		case strings.HasPrefix(line, "-"):
			rows = append(rows, row{num: take(&oldNo), text: line, class: removed})
		case strings.HasPrefix(line, `\`):
			rows = append(rows, row{text: line, class: note})
		default:
			n := take(&newNo)
			take(&oldNo)
			rows = append(rows, row{num: n, text: line, class: context})
		}
	}
	return rows
}

// hunk reads the lines the two files resume at out of a `@@ -12,5 +12,6 @@`
// header. Its counts are deliberately not read: every line below says which
// side it belongs to, so the two starts number the whole hunk on their own, and
// a count disagreeing with the body would be believed over the body.
func hunk(line string) (oldStart, newStart int, ok bool) {
	spec, found := strings.CutPrefix(line, "@@ ")
	if !found {
		return 0, 0, false
	}
	spec, _, found = strings.Cut(spec, " @@")
	if !found {
		return 0, 0, false
	}
	before, after, found := strings.Cut(spec, " ")
	if !found {
		return 0, 0, false
	}
	o, okOld := side(before, "-")
	n, okNew := side(after, "+")
	if !okOld || !okNew {
		return 0, 0, false
	}
	return o, n, true
}

// side is one half of a hunk header — `-12,5`, or `-12` where the count is one
// and the format leaves it out.
func side(field, sign string) (int, bool) {
	digits, found := strings.CutPrefix(field, sign)
	if !found {
		return 0, false
	}
	digits, _, _ = strings.Cut(digits, ",")
	n, err := strconv.Atoi(digits)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// gutter is the line-number column, as wide as the widest number the diff draws
// and empty for one whose lines nothing numbers.
//
// Tab stops are counted from the start of a line's content rather than from the
// row: every row carries the same gutter, so the indentation lines up either
// way, and counting the gutter in would draw the file's tabs at stops the file
// does not have.
type gutter struct{ digits int }

// measure sizes the gutter for the rows it will draw. Below a width that leaves
// a column for the line itself it is dropped whole: the numbers would otherwise
// be the only thing on the row, and past that the row runs wider than the
// terminal and wraps — the failure the cut exists to prevent.
func measure(rows []row, width int) gutter {
	widest := 0
	for _, r := range rows {
		widest = max(widest, r.num)
	}
	if widest == 0 {
		return gutter{}
	}
	digits := len(strconv.Itoa(widest))
	if width > 0 && digits+1 >= width {
		return gutter{}
	}
	return gutter{digits: digits}
}

// width is what the gutter takes from the terminal: its digits and the space
// after them.
func (g gutter) width() int {
	if g.digits == 0 {
		return 0
	}
	return g.digits + 1
}

// content is what is left for the line itself. A width of zero or less passes
// through as it came, since that is what fit reads as "nobody has said how wide
// the terminal is".
func (g gutter) content(width int) int {
	if width <= 0 {
		return width
	}
	return width - g.width()
}

func (g gutter) draw(r row, width int, p styles.Palette) string {
	if r.class == boundary {
		return strings.Repeat(" ", max(0, g.digits-1)) + style(r.class, p).Render(gap)
	}
	text, cut := fit(expand(r.text), g.content(width))
	drawn := g.number(r.num, p) + style(r.class, p).Render(text)
	if cut {
		drawn += p.Muted.Render(elision)
	}
	return drawn
}

// number is one gutter cell: a line's number right-aligned under the widest one
// in the diff, or the blank a line neither file numbers gets.
func (g gutter) number(n int, p styles.Palette) string {
	switch {
	case g.digits == 0:
		return ""
	case n == 0:
		return strings.Repeat(" ", g.width())
	}
	s := strconv.Itoa(n)
	return strings.Repeat(" ", g.digits-len(s)) + p.Muted.Render(s) + " "
}

func style(c class, p styles.Palette) lipgloss.Style {
	switch c {
	case added:
		return p.DiffAdded
	case removed:
		return p.DiffRemoved
	case note:
		return p.Muted
	case boundary:
		return p.DiffHunk
	}
	return p.DiffContext
}

// expand turns a diff line into something a terminal draws rather than acts on:
// tabs become spaces to the next stop, every other control character goes.
//
// One problem, since a file is arbitrary bytes and this draws them, and none of
// it is visible in the diff it corrupts. A tab measures as no cells, so a
// tab-indented line — most Go — measures short, survives the cut and wraps
// anyway. CR writes what follows back over the line and ends every line of a
// CRLF file, BEL sounds on every frame, ESC recolours the screen or swallows
// the rest of the row; C1 does that last one in a single byte, U+009B being CSI
// and U+0085 a line break, past where dropping ESC stops. And a bidi override
// reorders the row, so the card can show a guard clause the file does not
// contain — Trojan Source, aimed at the view a reader checks a change in.
// Dropping ESC alone leaves the rest of its sequence as inert literal text.
//
// Walked by grapheme cluster, which is what the column has to count: a flag
// emoji is two cells drawn and four summed per rune, so a tab after one lands
// at a stop the file does not have and every later tab inherits the drift.
// Counted as it goes, or a minified line pays a scan of itself per tab.
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

		// Bytes that are not text. A cluster walk hands these back as they came,
		// and the checks below decode a rune first, so a lone 0x9b — the one-byte
		// CSI named above — would go straight out. Replaced rather than dropped,
		// since U+FFFD is one inert cell saying something was there.
		case !utf8.ValidString(cluster):
			b.WriteRune(utf8.RuneError)
			col++

		case size == len(cluster) && (r < 0x20 || (r >= 0x7f && r <= 0x9f) || bidi(r)):
		default:
			b.WriteString(cluster)
			col += w
		}
	}
	return b.String()
}

// bidi reports the invisible directional formatting characters. The nine
// embeddings, overrides and isolates are the Trojan Source set; the three marks
// — LRM, RLM, ALM — reorder a neutral run by a weaker mechanism, to the same
// end. Tools disagree about the marks, rustc's text_direction_codepoint_in_
// literal covering only the nine, so the wider set is a choice made here: a
// code diff loses nothing by dropping them.
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
