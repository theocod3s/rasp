package styles

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Background is what the terminal is painted with, as the UI has been told. The
// zero value is Dark: a terminal answers the query asynchronously and a test has
// none to answer it, so every frame drawn before the answer — and every frame a
// golden records — comes from this value.
type Background int

const (
	Dark Background = iota
	Light
)

// Palette is the tokens a view draws through, resolved for one background.
type Palette struct {
	DiffAdded   lipgloss.Style
	DiffRemoved lipgloss.Style
	DiffContext lipgloss.Style
	DiffHunk    lipgloss.Style

	// Muted is what the UI put on the screen itself rather than what it was given
	// to show: the mark on a line the terminal was too narrow for, the note that
	// a file ends without a newline.
	Muted lipgloss.Style
}

// For returns the palette for bg. Anything that is not Light is dark, so a
// background nobody has reported resolves the same way the zero value does.
func For(bg Background) Palette {
	if bg == Light {
		return light
	}
	return dark
}

// Hex rather than the terminal's indexed colours, so the contrast against the
// background is decided here rather than inherited from whatever a theme
// redefined colour 2 to be. Bubble Tea's profile writer downgrades these on the
// way out to a terminal that cannot show them.
var light, dark = build(false), build(true)

func build(isDark bool) Palette {
	pick := lipgloss.LightDark(isDark)
	fg := func(onLight, onDark color.Color) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(pick(onLight, onDark))
	}
	return Palette{
		DiffAdded:   fg(lipgloss.Color("#1a7f37"), lipgloss.Color("#3fb950")),
		DiffRemoved: fg(lipgloss.Color("#cf222e"), lipgloss.Color("#f85149")),
		DiffContext: fg(lipgloss.Color("#57606a"), lipgloss.Color("#9198a1")),
		DiffHunk:    fg(lipgloss.Color("#0550ae"), lipgloss.Color("#58a6ff")),
		Muted:       fg(lipgloss.Color("#8c959f"), lipgloss.Color("#6e7681")),
	}
}
