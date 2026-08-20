package diffview

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/tui/styles"
)

const (
	// elision marks a line the terminal was too narrow for.
	elision = "…"

	// tabWidth is the stop tabs are expanded to. Four rather than the terminal's
	// usual eight because a diff is already two columns in — the card's indent
	// and the +/- marker — and eight puts a doubly-indented Go line past 80
	// columns on its whitespace alone.
	tabWidth = 4
)

// Render draws a unified diff, one screen line per line of it, in p's colours.
//
// Nothing wraps. A changed line broken across two rows reads as two changed
// lines, which is the one thing a diff must not say — so a line past width is
// cut at the edge and marked. The whole of it stays in the tool's Details,
// which is where a horizontal scroll will read it from; there is no binding for
// that yet, so today the mark is all a reader gets.
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

// body is the diff's lines with the `--- a/x` and `+++ b/x` pair above the
// first hunk dropped. The card this is drawn under already names the file, and
// at 80 columns two more lines of it is a third of what a small diff has to
// say. Positional rather than by prefix: a deleted line reading `-- x` is a
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

// classify reads a line's class off the character unified diff format puts it
// in: `+` inserted, `-` deleted, `@@` a hunk header, a space context. A `\`
// opens "\ No newline at end of file", which is the format talking about the
// file rather than a line out of it.
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
// survives the cut and wraps anyway. The rest a terminal obeys: an ESC lets a
// file recolour the screen or swallow what follows, a BEL sounds on every frame
// the line is in, and a CR writes the next characters back over it — which
// every line of every CRLF file ends with. Dropping the ESC alone leaves the
// rest of its sequence as literal text, visible and inert.
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
		case r < 0x20 || r == 0x7f:
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
// By grapheme cluster rather than by rune, which is not a detail: a flag emoji
// is two runes and two cells together but two cells *each* apart, and a sun
// plus the variation selector that makes it emoji is one rune of width and two
// cells drawn. Measuring by rune therefore calls a line too wide when it fits —
// cutting text and blaming the terminal — and calls one narrow enough when it
// is not, which is the wrap this exists to prevent.
func fit(line string, width int) (string, bool) {
	if width <= 0 || ansi.StringWidth(line) <= width {
		return line, false
	}
	return ansi.Truncate(line, width-ansi.StringWidth(elision), ""), true
}
