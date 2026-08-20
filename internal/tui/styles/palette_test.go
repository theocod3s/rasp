package styles_test

import (
	"image/color"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/tui/styles"
)

// TestEveryTokenIsADifferentColourInEachPalette is the light/dark criterion
// where it can be checked: a terminal answers the background query and a test
// has none, so the two palettes are compared to each other rather than to a
// screenshot. A token the same in both is one that was written once and never
// given its second colour, which shows on a real terminal as text the same
// shade as the background it is on.
func TestEveryTokenIsADifferentColourInEachPalette(t *testing.T) {
	light, dark := styles.For(styles.Light), styles.For(styles.Dark)

	v := reflect.TypeOf(light)
	if v.NumField() == 0 {
		t.Fatal("the palette has no tokens, so every comparison below holds vacuously")
	}
	for i := range v.NumField() {
		name := v.Field(i).Name
		t.Run(name, func(t *testing.T) {
			const sample = "sample"
			onLight := token(t, light, name).Render(sample)
			onDark := token(t, dark, name).Render(sample)

			switch {
			case onLight == onDark:
				t.Errorf("%s draws %q on both backgrounds", name, onLight)
			case !strings.Contains(onLight, "\x1b["), !strings.Contains(onDark, "\x1b["):
				t.Errorf("%s draws %q on light and %q on dark, one of them with no colour at all",
					name, onLight, onDark)
			}
		})
	}
}

// TestEveryTokenStandsOutFromTheBackgroundItIsFor is the assertion the test
// above cannot make. Two colours differing is not two colours being legible: a
// pair written the wrong way round — the light value on the dark palette — is
// distinct on both and invisible on both, and that is not hypothetical. The
// light Muted shipped at #8c959f, under 3:1 on white, with every assertion
// above green. It carries the mark saying a line was cut off, so a reader who
// cannot see it reads a shortened line as the whole of it.
//
// The floor is WCAG AA for normal text, which is what a diff is. The tokens
// clear it with room — 4.55:1 at the tightest — so it is a floor on the palette
// as chosen, not a bar it was tuned to scrape past; #8c959f came in at 3.04:1.
func TestEveryTokenStandsOutFromTheBackgroundItIsFor(t *testing.T) {
	const floor = 4.5

	// What each palette assumes it is drawn on: paper, and the near-black most
	// terminal themes settle on. Contrast is worse on a lighter dark background,
	// so the darker one is the generous case and this is a floor, not a promise.
	for _, bg := range []struct {
		name    string
		palette styles.Palette
		on      [3]float64
	}{
		{"light", styles.For(styles.Light), rgb(0xff, 0xff, 0xff)},
		{"dark", styles.For(styles.Dark), rgb(0x0d, 0x11, 0x17)},
	} {
		v := reflect.TypeOf(bg.palette)
		for i := range v.NumField() {
			name := v.Field(i).Name
			t.Run(bg.name+"/"+name, func(t *testing.T) {
				fg, ok := foreground(bg.palette, name)
				if !ok {
					t.Fatalf("%s sets no foreground colour, so there is no contrast to measure", name)
				}
				if got := contrast(fg, bg.on); got < floor {
					t.Errorf("%s draws at %.2f:1 on a %s background, under the %.1f:1 floor — most "+
						"likely the light and dark values are the wrong way round", name, got, bg.name, floor)
				}
			})
		}
	}
}

// foreground is a token's colour as three channels in 0..1.
func foreground(p styles.Palette, name string) ([3]float64, bool) {
	c := reflect.ValueOf(p).FieldByName(name).Interface().(interface{ GetForeground() color.Color })
	fg := c.GetForeground()
	if fg == nil {
		return [3]float64{}, false
	}
	r, g, b, a := fg.RGBA()
	if a == 0 {
		return [3]float64{}, false
	}
	return [3]float64{float64(r) / 65535, float64(g) / 65535, float64(b) / 65535}, true
}

func rgb(r, g, b uint8) [3]float64 {
	return [3]float64{float64(r) / 255, float64(g) / 255, float64(b) / 255}
}

// contrast is the WCAG ratio between two colours, from 1 (identical) to 21.
func contrast(a, b [3]float64) float64 {
	x, y := luminance(a)+0.05, luminance(b)+0.05
	if x < y {
		x, y = y, x
	}
	return x / y
}

// luminance is WCAG relative luminance: each channel linearised out of sRGB,
// then weighted by how much the eye takes from it.
func luminance(c [3]float64) float64 {
	lin := func(v float64) float64 {
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c[0]) + 0.7152*lin(c[1]) + 0.0722*lin(c[2])
}

// TestABackgroundNobodyReportedDrawsTheDarkPalette. The query is answered after
// the program is already drawing, and on a terminal that does not answer it
// never is — so the zero value has to be a whole palette rather than a blank
// one.
func TestABackgroundNobodyReportedDrawsTheDarkPalette(t *testing.T) {
	var unreported styles.Background

	if got, want := styles.For(unreported), styles.For(styles.Dark); !reflect.DeepEqual(got, want) {
		t.Error("the zero Background does not resolve to the dark palette")
	}
	if got, want := styles.For(styles.Background(42)), styles.For(styles.Dark); !reflect.DeepEqual(got, want) {
		t.Error("a Background that is neither constant resolves to something other than the dark palette")
	}
}

func token(t *testing.T, p styles.Palette, name string) interface{ Render(...string) string } {
	t.Helper()

	field := reflect.ValueOf(p).FieldByName(name).Interface()
	style, ok := field.(interface{ Render(...string) string })
	if !ok {
		t.Fatalf("%s is a %T, and a palette holds styles", name, field)
	}
	return style
}
