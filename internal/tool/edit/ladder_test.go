package edit_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/tool/edit"
)

// twice holds the same statement in two functions, which is the shape the
// ambiguity rung exists for: text a model reproduced faithfully from one place
// and could equally have read from the other.
const (
	twice = "func serve() {\n\tport := 8080\n}\n\nfunc probe() {\n\tport := 8080\n}\n"

	firstMatchWins = "func serve() {\n\tport := 9090\n}\n\nfunc probe() {\n\tport := 8080\n}\n"
	lastMatchWins  = "func serve() {\n\tport := 8080\n}\n\nfunc probe() {\n\tport := 9090\n}\n"
	bothReplaced   = "func serve() {\n\tport := 9090\n}\n\nfunc probe() {\n\tport := 9090\n}\n"
)

func TestExactMatchReplaces(t *testing.T) {
	tests := map[string]struct {
		src, old, new string
		replaceAll    bool
		want          string
		wantCount     int
	}{
		"single occurrence": {
			src: "a\nb\nc\n", old: "b", new: "B",
			want: "a\nB\nc\n", wantCount: 1,
		},
		"indentation is part of the match": {
			src:  "if x {\n\t\treturn 1\n}\n",
			old:  "\t\treturn 1",
			new:  "\t\treturn 2",
			want: "if x {\n\t\treturn 2\n}\n", wantCount: 1,
		},
		"deleting text": {
			src: "keep\ndrop\nkeep\n", old: "drop\n", new: "",
			want: "keep\nkeep\n", wantCount: 1,
		},
		"replacement containing the old text": {
			src: "log(x)\n", old: "log(x)", new: "log(x); log(y)",
			want: "log(x); log(y)\n", wantCount: 1,
		},
		"replace_all takes every occurrence": {
			src: twice, old: "8080", new: "9090", replaceAll: true,
			want: bothReplaced, wantCount: 2,
		},
		"replace_all with one occurrence": {
			src: "a\nb\n", old: "b", new: "c", replaceAll: true,
			want: "a\nc\n", wantCount: 1,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rep, err := edit.Apply(tc.src, tc.old, tc.new, tc.replaceAll)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if rep.Text != tc.want {
				t.Errorf("text = %q, want %q", rep.Text, tc.want)
			}
			if rep.Count != tc.wantCount {
				t.Errorf("count = %d, want %d", rep.Count, tc.wantCount)
			}
			if rep.Rung != edit.Exact {
				t.Errorf("rung = %d, want %d (Exact) — a byte-exact match is rung 1",
					rep.Rung, edit.Exact)
			}
		})
	}
}

// TestAmbiguousMatchIsRefused is the ticket's heart, so it asserts the two wrong
// answers by name: a ladder that quietly picked one of the occurrences would
// still return a plausible-looking file, and only the content says which.
func TestAmbiguousMatchIsRefused(t *testing.T) {
	rep, err := edit.Apply(twice, "8080", "9090", false)

	var ambiguous *edit.AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("Apply(...) error = %v, want *edit.AmbiguousError", err)
	}
	if ambiguous.Count != 2 {
		t.Errorf("count = %d, want 2; the count is what tells the model how much "+
			"context it has to add", ambiguous.Count)
	}

	switch rep.Text {
	case firstMatchWins:
		t.Fatal("first match wins: the ladder replaced the port in serve() and left probe() " +
			"holding the old one. Two occurrences without replace_all is a refusal")
	case lastMatchWins:
		t.Fatal("last match wins: the ladder replaced the port in probe() and left serve() " +
			"holding the old one. Two occurrences without replace_all is a refusal")
	case bothReplaced:
		t.Fatal("every occurrence was replaced without replace_all being set")
	case "":
	default:
		t.Fatalf("a refused edit produced text: %q", rep.Text)
	}

	if rep.Count != 0 || rep.Rung != 0 {
		t.Errorf("a refused edit reported count %d on rung %d", rep.Count, rep.Rung)
	}
}

// TestOverlappingOccurrencesCountAsTheyReplace pins the one place the count and
// the replacement could disagree: both are non-overlapping left-to-right scans,
// so "aa" is in "aaa" once, not twice, and a count taken any other way would
// refuse an edit that is not ambiguous.
func TestOverlappingOccurrencesCountAsTheyReplace(t *testing.T) {
	rep, err := edit.Apply("aaa\n", "aa", "b", false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.Text != "ba\n" {
		t.Errorf("text = %q, want %q", rep.Text, "ba\n")
	}
}

func TestNoMatchFallsThrough(t *testing.T) {
	tests := map[string]struct {
		src, old, new string
		want          error
	}{
		"text is not there": {
			src: "a\nb\n", old: "c", want: edit.ErrNotFound,
		},
		"whitespace differs": {
			// The rung that would accept this is the normalized one, so until it
			// exists a whitespace mismatch has to leave the ladder as a clean miss
			// rather than as a match with the indentation guessed.
			src: "if x {\n\treturn 1\n}\n", old: "    return 1", new: "    return 2",
			want: edit.ErrNotFound,
		},
		"nothing to match": {
			src: "a\n", old: "", new: "b", want: edit.ErrEmpty,
		},
		"replacement is the original": {
			src: "a\n", old: "a", new: "a", want: edit.ErrUnchanged,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rep, err := edit.Apply(tc.src, tc.old, tc.new, false)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Apply(...) error = %v, want %v", err, tc.want)
			}
			if rep != (edit.Replacement{}) {
				t.Errorf("a refused edit produced %+v", rep)
			}
		})
	}
}

// FuzzApply hunts for the outcome design §13 names as the worst one: a match
// that succeeds in the wrong place. The oracle is strings.Split/Join, which
// reaches the same answer as Apply's strings.Replace by a different route, so an
// off-by-one in either shows up as a disagreement rather than as agreement about
// the wrong bytes.
func FuzzApply(f *testing.F) {
	f.Add("package main\n\nfunc main() {}\n", "main() {}", "main() { run() }", false)
	f.Add(twice, "8080", "9090", true)
	f.Add(twice, "8080", "9090", false)
	f.Add("aaa", "aa", "b", false)
	f.Add("", "x", "y", false)
	f.Add("x", "x", "", false)
	f.Add("\t\tif err != nil {\n", "\t\tif err != nil {\n", "\t\tif err == nil {\n", false)

	f.Fuzz(func(t *testing.T, src, old, new string, replaceAll bool) {
		rep, err := edit.Apply(src, old, new, replaceAll)

		count := strings.Count(src, old)
		if err != nil {
			if rep != (edit.Replacement{}) {
				t.Fatalf("Apply(%q, %q, %q, %v) failed with %v but still produced %+v",
					src, old, new, replaceAll, err, rep)
			}
			var ambiguous *edit.AmbiguousError
			if errors.As(err, &ambiguous) && (ambiguous.Count != count || count < 2 || replaceAll) {
				t.Fatalf("Apply(%q, %q, %q, %v) called %d occurrences ambiguous; src holds %d",
					src, old, new, replaceAll, ambiguous.Count, count)
			}
			return
		}

		switch {
		case count == 0:
			t.Fatalf("Apply(%q, %q, %q, %v) matched text that is not there",
				src, old, new, replaceAll)
		case count > 1 && !replaceAll:
			t.Fatalf("Apply(%q, %q, %q, %v) replaced %d occurrences without replace_all",
				src, old, new, replaceAll, count)
		case rep.Count != count:
			t.Fatalf("Apply(%q, %q, %q, %v) reported %d occurrences, src holds %d",
				src, old, new, replaceAll, rep.Count, count)
		case rep.Rung != edit.Exact:
			t.Fatalf("Apply(%q, %q, %q, %v) matched on rung %d, and rung %d is the only one",
				src, old, new, replaceAll, rep.Rung, edit.Exact)
		}

		if want := strings.Join(strings.Split(src, old), new); rep.Text != want {
			t.Fatalf("Apply(%q, %q, %q, %v) = %q, want %q",
				src, old, new, replaceAll, rep.Text, want)
		}
	})
}
