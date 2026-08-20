package diffview

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/tui/styles"
)

const (
	elision = "…"

	// Four rather than the terminal's usual eight: a diff is already two columns
	// in, between the card's indent and the +/- marker, and eight puts a
	// doubly-indented Go line past 80 columns on its whitespace alone.
	tabWidth = 4
)

// Render draws a unified diff, one screen line per line of it, in p's colours.
// Nothing wraps: a changed line broken across two rows reads as two changed
// lines, so a line past width is cut and marked instead. The whole of it stays
// in the tool's Details, where a horizontal scroll will read it from — there is
// no binding for that yet, so the mark is all a reader gets today.
//
// A width of zero or less is a terminal that has not reported its size, and
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
// asking and drawing cannot disagree: what counts as nothing to show is the
// header-stripping below, and a caller deciding for itself would call a
// header-only diff a diff and open a card onto an empty body.
func Draws(unified string) bool { return len(body(unified)) > 0 }

// body is the diff's lines with the `--- a/x` and `+++ b/x` pair above the
// first hunk dropped, since the card this is drawn under already names the
// file. Positional rather than by prefix: a deleted line reading `-- x` is a
// deletion, and it can only be one below a hunk header.
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

// classify reads a line's class off the character the format puts it in. The
// `\` is the one that is not a class of source line: it opens "\ No newline at
// end of file", the format talking about the file rather than a line out of it.
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
// One problem, since a file is arbitrary bytes and this draws them. A tab
// measures as no cells, so a tab-indented line — most Go — measures short,
// survives the cut and wraps anyway. The rest a terminal obeys: ESC lets a file
// recolour the screen or swallow what follows, BEL sounds on every frame, CR
// writes the next characters back over the line — and every line of a CRLF file
// ends with one. C1 goes too, since U+009B is CSI on its own and U+0085 a line
// break, both of them past where dropping ESC would stop. Dropping ESC alone
// leaves the rest of its sequence as literal text, visible and inert.
func expand(line string) string {
	var (
		b   strings.Builder
		col int
	)
	for _, r := range line {
		switch {
		case r == '\t':
			n := tabWidth - col%tabWidth
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
		default:
			w := 1
			if r > 0x7f {
				w = ansi.StringWidth(string(r))
			}
			b.WriteRune(r)
			col += w
		}
	}
	return b.String()
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
