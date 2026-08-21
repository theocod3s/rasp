package chat

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/theocod3s/rasp/internal/tui/styles"
)

// wordmark is "RASP" as five rows of block glyphs, authored here rather than
// produced by a figlet library at draw time. Every row is the same width in
// runes — a full block and a space are both one terminal cell — which is what
// lets gradient, below, assign one colour per column.
var wordmark = [5]string{
	"████   ███   ████ ████ ",
	"█   █ █   █ █     █   █",
	"████  █████  ███  ████ ",
	"█ █   █   █     █ █    ",
	"█  █  █   █ ████  █    ",
}

var wordmarkWidth = len([]rune(wordmark[0]))

// bannerMargin sets the whole block one column in from the transcript's edge.
const bannerMargin = " "

// Banner is the session's identity block: the wordmark, the run as four
// labeled rows, then the hint that ends it. It is appended once as the first
// item of the conversation and never replaced, so Finished is always true —
// the view freezes it at the first width it draws to (view.go).
type Banner struct {
	Version, Model, Mode, Cwd string

	// NoColor is read once by whoever builds this item, from the environment —
	// checking it again on every Render would let a variable that changed after
	// start-up repaint a line the freeze already told the reader was final.
	NoColor bool

	Background styles.Background
}

func (Banner) Finished() bool { return true }

// Render draws the wordmark in a gradient when there is room for it and colour
// is wanted; a terminal too narrow for the art, or one told NO_COLOR, gets the
// plain bold word in place of the art clipped or wrapped into noise.
func (b Banner) Render(width int) string {
	var lines []string
	if b.fits(width) {
		lines = b.gradient()
	} else {
		lines = []string{lipgloss.NewStyle().Bold(true).Render("Rasp")}
	}

	muted := styles.For(b.Background).Muted
	lines = append(lines, "",
		b.row("version", b.Version), b.row("model", b.Model), b.row("mode", b.Mode), b.row("cwd", b.Cwd),
		"", muted.Render("type /help for slash commands"))

	for i, line := range lines {
		if line != "" {
			lines[i] = bannerMargin + line
		}
	}
	return strings.Join(lines, "\n")
}

// fits is whether the terminal has measured itself wide enough for the art and
// nothing has asked to go without colour. Width zero or less is a size that has
// not arrived yet — the same reading wrap gives it elsewhere in this package —
// so the art is drawn rather than assumed too big for a terminal nothing has
// measured.
func (b Banner) fits(width int) bool {
	if b.NoColor {
		return false
	}
	return width <= 0 || width >= len(bannerMargin)+wordmarkWidth
}

// gradient draws wordmark through lipgloss.Blend1D over the palette's own
// three stops, left to right, so the colours are the ones the contrast test
// already checked rather than a second set chosen here.
func (b Banner) gradient() []string {
	palette := styles.For(b.Background)
	stops := lipgloss.Blend1D(wordmarkWidth,
		palette.BannerFrom.GetForeground(), palette.BannerVia.GetForeground(), palette.BannerTo.GetForeground())

	lines := make([]string, len(wordmark))
	for r, row := range wordmark {
		var line strings.Builder
		// By rune, not by range's own byte index: a full block is three bytes
		// wide in UTF-8 and one column wide on screen, and stops is indexed by
		// column.
		for i, ch := range []rune(row) {
			if ch == ' ' {
				line.WriteByte(' ')
				continue
			}
			line.WriteString(lipgloss.NewStyle().Foreground(stops[i]).Render(string(ch)))
		}
		lines[r] = line.String()
	}
	return lines
}

// row is one labeled line, the label padded to a fixed column so every value
// starts flush whatever it is named.
func (b Banner) row(label, value string) string {
	return fmt.Sprintf("  %-10s%s", label, value)
}
