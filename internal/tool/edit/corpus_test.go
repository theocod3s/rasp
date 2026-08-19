package edit_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/tool/edit"
)

// corpusCase is one edit the ladder has to answer, and the single outcome it
// must answer with.
type corpusCase struct {
	name string

	// provenance is where the case came from: "hand-written: taxonomy/crlf",
	// later "dogfood: <session>". A case whose origin is lost is a case nobody
	// can decide the fate of when the ladder changes under it — kept because it
	// came off a real transcript, or dropped because it was always a guess.
	provenance string

	src, old, new string
	replaceAll    bool

	// A case names a replacement — rung, want and count — or it names err, the
	// refusal, with the field that refusal reports. Never both, and never
	// neither: an outcome left blank is a case that passes without asserting.
	rung  edit.Rung
	want  string
	count int
	err   error
}

// outcome names the ladder behaviour a case reaches, and answers "" for a case
// that reaches none — an unclassified refusal asserts less than it appears to.
func (c corpusCase) outcome() string {
	var ambiguous *edit.AmbiguousError
	var notFound *edit.NotFoundError
	switch {
	case c.err == nil && c.rung == edit.Exact:
		return "the exact rung"
	case c.err == nil && c.rung == edit.Normalized:
		return "the normalized rung"
	case errors.As(c.err, &ambiguous):
		return "an ambiguity refusal"
	case errors.As(c.err, &notFound) && notFound.Line != 0:
		return "a near miss with a location"
	case errors.As(c.err, &notFound):
		return "a near miss with nowhere to point"
	case errors.Is(c.err, edit.ErrEmpty):
		return "ErrEmpty"
	case errors.Is(c.err, edit.ErrUnchanged):
		return "ErrUnchanged"
	}
	return ""
}

// taxonomy is the failure list the corpus was seeded from. A branch nothing
// covers is a branch the ladder stopped being measured on, which reads from a
// green run exactly like a branch that works.
var taxonomy = []string{"whitespace-drift", "smart-quotes", "crlf", "reindentation", "ambiguity"}

func TestCorpus(t *testing.T) {
	if len(corpus) == 0 {
		t.Fatal("the corpus is empty, so every case below it passes by never running")
	}

	names := make(map[string]bool, len(corpus))
	reached := make(map[string]int, len(corpus))
	for i, c := range corpus {
		switch {
		case c.name == "":
			t.Errorf("corpus[%d] has no name", i)
		case names[c.name]:
			// t.Run appends a suffix rather than complaining, so a repeat is two
			// subtests one of which nobody can name.
			t.Errorf("corpus[%d] repeats the name %q", i, c.name)
		}
		names[c.name] = true

		if c.provenance == "" {
			t.Errorf("%s: no provenance", c.name)
		}
		if c.err != nil && (c.rung != 0 || c.want != "" || c.count != 0) {
			t.Errorf("%s: names the refusal %v and a replacement on rung %d", c.name, c.err, c.rung)
		}
		if c.err == nil && (c.rung == 0 || c.count == 0) {
			t.Errorf("%s: names neither a rung nor a refusal, so it asserts nothing", c.name)
		}
		if got := c.outcome(); got == "" {
			t.Errorf("%s: %v is not one of the ladder's outcomes; a near miss goes in as a "+
				"*edit.NotFoundError with the line it points at, not as the bare sentinel", c.name, c.err)
		} else {
			reached[got]++
		}
	}

	for _, want := range []string{
		"the exact rung", "the normalized rung", "an ambiguity refusal",
		"a near miss with a location", "a near miss with nowhere to point",
		"ErrEmpty", "ErrUnchanged",
	} {
		if reached[want] == 0 {
			t.Errorf("no case in the corpus reaches %s", want)
		}
	}
	for _, tag := range taxonomy {
		if !slices.ContainsFunc(corpus, func(c corpusCase) bool {
			return strings.Contains(c.provenance, "taxonomy/"+tag)
		}) {
			t.Errorf("no case carries provenance taxonomy/%s", tag)
		}
	}

	for _, c := range corpus {
		t.Run(c.name, func(t *testing.T) {
			rep, err := edit.Apply(c.src, c.old, c.new, c.replaceAll)
			if c.err != nil {
				checkRefusal(t, c, rep, err)
				return
			}

			if err != nil {
				t.Fatalf("Apply(%q, %q, %q, %v) failed with %v; the corpus has it landing on rung %d",
					c.src, c.old, c.new, c.replaceAll, err, c.rung)
			}
			if rep.Rung != c.rung {
				t.Errorf("rung = %d, want %d", rep.Rung, c.rung)
			}
			if rep.Text != c.want {
				t.Errorf("text = %q, want %q", rep.Text, c.want)
			}
			if rep.Count != c.count {
				t.Errorf("count = %d, want %d", rep.Count, c.count)
			}
		})
	}
}

func checkRefusal(t *testing.T, c corpusCase, rep edit.Replacement, err error) {
	t.Helper()

	if rep != (edit.Replacement{}) {
		t.Errorf("a refused edit produced %+v", rep)
	}

	var wantAmbiguous *edit.AmbiguousError
	var wantNotFound *edit.NotFoundError
	switch {
	case errors.As(c.err, &wantAmbiguous):
		var got *edit.AmbiguousError
		if !errors.As(err, &got) {
			t.Fatalf("Apply(%q, %q, %q, %v) error = %v, want *edit.AmbiguousError",
				c.src, c.old, c.new, c.replaceAll, err)
		}
		if got.Count != wantAmbiguous.Count {
			t.Errorf("count = %d, want %d", got.Count, wantAmbiguous.Count)
		}

	case errors.As(c.err, &wantNotFound):
		var got *edit.NotFoundError
		if !errors.As(err, &got) {
			t.Fatalf("Apply(%q, %q, %q, %v) error = %v, want *edit.NotFoundError",
				c.src, c.old, c.new, c.replaceAll, err)
		}
		if !errors.Is(err, edit.ErrNotFound) {
			t.Error("a miss that stopped answering to ErrNotFound, which is what a caller that " +
				"only cares whether the ladder failed asks with")
		}
		if got.Line != wantNotFound.Line {
			t.Errorf("line = %d, want %d", got.Line, wantNotFound.Line)
		}
		if (got.Line == 0) != (got.Actual == "") {
			t.Errorf("line %d quoted %q back; a location and its content arrive together or not at all",
				got.Line, got.Actual)
		}
		// Actual is asserted where a case spells it out. The rendering itself is
		// pinned once, in the near-miss tests; here it is the location that has to
		// be right, and a case that also states the content says why.
		if wantNotFound.Actual != "" && got.Actual != wantNotFound.Actual {
			t.Errorf("actual =\n%q\nwant\n%q", got.Actual, wantNotFound.Actual)
		}

	default:
		if !errors.Is(err, c.err) {
			t.Fatalf("Apply(%q, %q, %q, %v) error = %v, want %v",
				c.src, c.old, c.new, c.replaceAll, err, c.err)
		}
	}
}
