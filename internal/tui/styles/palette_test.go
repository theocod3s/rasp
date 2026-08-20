package styles_test

import (
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
