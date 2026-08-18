package llm_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/theocod3s/rasp/internal/llm"
)

// TestProviderRequiresEfforts checks the method over the interface. A build stays
// green if it is dropped from Provider and left on the adapters, and what would
// then be gone is the guarantee that every future adapter has one — an adapter
// without a list reads as an adapter that allows every rung.
//
// Its emptiness is the other half: a method taking a model id could not be
// answered without a catalog, which rasp does not have and will not get.
func TestProviderRequiresEfforts(t *testing.T) {
	method, ok := reflect.TypeFor[llm.Provider]().MethodByName("Efforts")
	if !ok {
		t.Fatal("Provider has no Efforts method; an adapter that published no list would satisfy it")
	}
	if got := method.Type.NumIn(); got != 0 {
		t.Errorf("Provider.Efforts takes %d arguments, want none: the list is per protocol, "+
			"and answering per model needs a catalog", got)
	}
	if got, want := method.Type.NumOut(), 1; got != want {
		t.Fatalf("Provider.Efforts returns %d values, want %d", got, want)
	}
	if got, want := method.Type.Out(0), reflect.TypeFor[[]llm.Effort](); got != want {
		t.Errorf("Provider.Efforts returns %s, want %s", got, want)
	}
}

// TestEffortLadder pins the rungs and their order against the literal strings
// rather than the constants: these are the neutral spellings, the ones a config
// key and a saved session will carry, so renaming one is a migration and not a
// refactor. What goes on the wire is the adapter's own mapping and need not
// match. The order is what "ladder order" means to every Provider.Efforts and
// every picker reading one.
func TestEffortLadder(t *testing.T) {
	want := []llm.Effort{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

	got := llm.EffortLadder()
	if !slices.Equal(got, want) {
		t.Fatalf("EffortLadder() = %v, want %v", got, want)
	}

	// An adapter derives its list from this one and a refusal reads that list, so
	// a caller sorting the result in place would rewrite both.
	got[0] = "tampered"
	if again := llm.EffortLadder(); !slices.Equal(again, want) {
		t.Errorf("EffortLadder() = %v after a caller wrote to an earlier result, want %v", again, want)
	}
}
