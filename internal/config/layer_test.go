package config

// Internal, because its subject is the layer classification itself rather than
// anything the package exposes.

import "testing"

// TestEveryLayerIsClassified fails when a layer is added without deciding
// whether values from it hold a recipe or a literal.
//
// Both wrong answers are silent, and neither produces a compile error: a file
// layer left out returns `$(op read …)` to the provider as an API key, and a
// shell-sourced layer let in misreads a key containing a dollar.
func TestEveryLayerIsClassified(t *testing.T) {
	want := map[Layer]bool{
		LayerDefault: false, // compiled in; we would write the value, not a recipe
		LayerGlobal:  true,
		LayerProject: true,
		LayerEnv:     false, // already through a shell
		LayerFlag:    false, // likewise
	}

	if len(want) != len(layers) {
		t.Fatalf("%d layers exist but %d are classified — a new one needs a decision in "+
			"writtenInAFile, and a reason on this line", len(layers), len(want))
	}
	for _, l := range layers {
		expected, named := want[l]
		if !named {
			t.Errorf("layer %v is not classified", l)
			continue
		}
		if got := writtenInAFile(l); got != expected {
			t.Errorf("writtenInAFile(%v) = %v, want %v", l, got, expected)
		}
	}
}
