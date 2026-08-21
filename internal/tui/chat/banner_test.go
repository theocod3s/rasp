package chat_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/tui/chat"
)

func demoBanner() chat.Banner {
	return chat.Banner{Version: "v0.2.0", Model: "anthropic/claude-opus-5", Mode: "manual", Cwd: "~/scratch/rasp-demo"}
}

// TestABannerDrawsEveryFieldVerbatim is what "one source, no second copy"
// rests on at this layer: the item reformats none of Version, Model, Mode or
// Cwd, so whatever tui.go hands it is exactly what a reader sees.
func TestABannerDrawsEveryFieldVerbatim(t *testing.T) {
	b := demoBanner()
	drawn := words(b.Render(wide))
	for _, want := range []string{b.Version, b.Model, b.Mode, b.Cwd} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the frame does not contain %q:\n%s", want, drawn)
		}
	}
}

// TestABannerDrawsTheArtWhenThereIsRoomAndColour is the ordinary case: wide
// enough, and nothing has asked to go without colour.
func TestABannerDrawsTheArtWhenThereIsRoomAndColour(t *testing.T) {
	drawn := demoBanner().Render(wide)
	if !strings.Contains(drawn, "█") {
		t.Fatalf("a banner at %d columns drew no art:\n%s", wide, drawn)
	}
	if !strings.Contains(drawn, "\x1b[38;2") {
		t.Errorf("the art carries no true-colour escape, so the gradient never ran:\n%s", drawn)
	}
}

// TestABannerDegradesWhenTheTerminalIsTooNarrow is the honesty the ticket asks
// for: art clipped mid-glyph or wrapped across lines is worse than no art, so a
// terminal without room for it gets the plain word instead.
func TestABannerDegradesWhenTheTerminalIsTooNarrow(t *testing.T) {
	const narrow = 10

	drawn := demoBanner().Render(narrow)
	if strings.Contains(drawn, "█") {
		t.Fatalf("the banner still drew block art at %d columns:\n%s", narrow, drawn)
	}
	first, _, _ := strings.Cut(drawn, "\n")
	if got := ansi.Strip(first); strings.TrimSpace(got) != "Rasp" {
		t.Errorf("the first line reads %q, want the plain word Rasp", got)
	}
	if n := ansi.StringWidth(first); n > narrow {
		t.Errorf("the fallback line runs %d columns into a %d-wide terminal", n, narrow)
	}
}

// TestABannerDegradesUnderNoColorEvenWhenWide is the other half of honest
// degradation: coloured art with the colour stripped out from under it is not
// what NO_COLOR asks for, so NoColor swaps the whole block for the plain word
// rather than leave the art standing in monochrome.
func TestABannerDegradesUnderNoColorEvenWhenWide(t *testing.T) {
	b := demoBanner()
	b.NoColor = true

	drawn := b.Render(wide)
	if strings.Contains(drawn, "█") {
		t.Fatalf("the banner drew block art under NoColor at %d columns:\n%s", wide, drawn)
	}
	first, _, _ := strings.Cut(drawn, "\n")
	if got := ansi.Strip(first); strings.TrimSpace(got) != "Rasp" {
		t.Errorf("the first line reads %q, want the plain word Rasp", got)
	}
}

// TestABannerRendersOnceThenFreezes extends the freeze-test family
// (view_test.go) to the item this file adds: Banner answers Finished with a
// constant true, and a Render called more than once for it at one width would
// be the bug that constant exists to rule out.
func TestABannerRendersOnceThenFreezes(t *testing.T) {
	var runs int
	var v chat.View
	v.Append(countedBanner{Banner: demoBanner(), runs: &runs})

	first := v.Render(wide)
	for range 5 {
		if got := v.Render(wide); got != first {
			t.Fatalf("a later frame reads %q, and the banner was frozen at %q", got, first)
		}
	}
	if runs != 1 {
		t.Errorf("the banner rendered %d time(s) across six frames at one width, want 1", runs)
	}
}

// TestABannerResizedNarrowRedrawsAsThePlainWord is the freeze cache meeting
// this item's own degradation: the cache is keyed by width (view.go), so a
// terminal that shrank after the first frame has to draw the plain word on the
// next one rather than serve back a cached line of art that no longer fits.
func TestABannerResizedNarrowRedrawsAsThePlainWord(t *testing.T) {
	var v chat.View
	v.Append(demoBanner())

	wideFrame := v.Render(wide)
	if !strings.Contains(wideFrame, "█") {
		t.Fatalf("the wide frame drew no art to begin with:\n%s", wideFrame)
	}

	narrowFrame := v.Render(10)
	if strings.Contains(narrowFrame, "█") {
		t.Errorf("the banner still drew art after a resize to 10 columns:\n%s", narrowFrame)
	}

	if back := v.Render(wide); !strings.Contains(back, "█") {
		t.Errorf("back at %d columns the banner drew no art:\n%s", wide, back)
	}
}

// countedBanner renders exactly as a Banner does and records how often it was
// asked to, the way counted does for a Message (view_test.go).
type countedBanner struct {
	chat.Banner
	runs *int
}

func (c countedBanner) Render(width int) string {
	*c.runs++
	return c.Banner.Render(width)
}
