package edit_test

import (
	"errors"
	"slices"
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
// that succeeds in the wrong place. Both matching rungs get an oracle that
// reaches the same answer by a different route — strings.Split/Join against the
// exact rung's strings.Replace, and a substring search over the file's trimmed
// lines against the normalized rung's line-by-line scan — so an off-by-one in
// either shows up as a disagreement rather than as agreement about the wrong
// bytes.
func FuzzApply(f *testing.F) {
	f.Add("package main\n\nfunc main() {}\n", "main() {}", "main() { run() }", false)
	f.Add(twice, "8080", "9090", true)
	f.Add(twice, "8080", "9090", false)
	f.Add("aaa", "aa", "b", false)
	f.Add("", "x", "y", false)
	f.Add("x", "x", "", false)
	f.Add("\t\tif err != nil {\n", "\t\tif err != nil {\n", "\t\tif err == nil {\n", false)
	f.Add("func f() {\n\tif x {\n\t\treturn 1\n\t}\n}\n",
		"    if x {\n        return 1\n    }", "    if x {\n        return 2\n    }", false)
	f.Add("\tport := 8080\n", "port := 8080  ", "port := 9090", false)
	f.Add("a\r\nb\r\nc\r\n", "  b", "B", false)
	// Four spaces against the file's tabs: "p := 1" on its own is found twice byte
	// for byte, inside the indentation of both lines, and never reaches the rung
	// this seed is for.
	f.Add("func a() {\n\tp := 1\n}\n\nfunc b() {\n\t\tp := 1\n}\n", "    p := 1", "    p := 2", true)
	f.Add("func a() {\n\tp := 1\n}\n\nfunc b() {\n\t\tp := 1\n}\n", "    p := 1", "    p := 2", false)
	f.Add("x\ny\nz\n", "   \n", "q", false)
	f.Add("one\ntwo\nthree\n", "one\nTWO\nthree", "1\n2\n3", false)
	f.Add("keep\n\tdrop\nkeep\n", "    drop\n", "", false)

	f.Fuzz(func(t *testing.T, src, old, new string, replaceAll bool) {
		rep, err := edit.Apply(src, old, new, replaceAll)

		exact := strings.Count(src, old)
		var aligned []int
		if old != "" && exact == 0 {
			aligned = alignedOracle(src, old)
		}

		if err != nil {
			if rep != (edit.Replacement{}) {
				t.Fatalf("Apply(%q, %q, %q, %v) failed with %v but still produced %+v",
					src, old, new, replaceAll, err, rep)
			}

			var ambiguous *edit.AmbiguousError
			var notFound *edit.NotFoundError
			switch {
			case errors.As(err, &ambiguous):
				want := exact
				if exact == 0 {
					want = len(aligned)
				}
				if ambiguous.Count != want || want < 2 || replaceAll {
					t.Fatalf("Apply(%q, %q, %q, %v) called %d occurrences ambiguous; src holds %d "+
						"exact and %d normalized", src, old, new, replaceAll, ambiguous.Count, exact, len(aligned))
				}
			case errors.As(err, &notFound):
				if len(aligned) > 0 {
					t.Fatalf("Apply(%q, %q, %q, %v) found nothing, but %d whole-line normalized "+
						"matches are there at %v", src, old, new, replaceAll, len(aligned), aligned)
				}
				if lines := len(rawLines(src)); notFound.Line < 0 || notFound.Line > lines {
					t.Fatalf("Apply(%q, %q, %q, %v) pointed at line %d of a %d-line file",
						src, old, new, replaceAll, notFound.Line, lines)
				}
				if (notFound.Line == 0) != (notFound.Actual == "") {
					t.Fatalf("Apply(%q, %q, %q, %v) reported line %d with content %q; a location "+
						"and its content arrive together or not at all",
						src, old, new, replaceAll, notFound.Line, notFound.Actual)
				}
				if strings.Contains(notFound.Actual, "\t") {
					t.Fatalf("Apply(%q, %q, %q, %v) quoted %q back with a real tab in it, which is "+
						"as invisible as it was in the file", src, old, new, replaceAll, notFound.Actual)
				}
			case errors.Is(err, edit.ErrEmpty):
				if old != "" {
					t.Fatalf("Apply(%q, %q, %q, %v) called %q empty", src, old, new, replaceAll, old)
				}
			case errors.Is(err, edit.ErrUnchanged):
				if old != new && len(aligned) == 0 {
					t.Fatalf("Apply(%q, %q, %q, %v) said the edit changes nothing, but old and new "+
						"differ and nothing matched", src, old, new, replaceAll)
				}
			default:
				t.Fatalf("Apply(%q, %q, %q, %v) failed with %v, which is not one of the ladder's "+
					"refusals — every miss carries a *NotFoundError so a caller can always ask "+
					"where the file came closest", src, old, new, replaceAll, err)
			}
			return
		}

		if rep.Text == src {
			t.Fatalf("Apply(%q, %q, %q, %v) reported %d replacements on rung %d and left src as it was",
				src, old, new, replaceAll, rep.Count, rep.Rung)
		}

		switch rep.Rung {
		case edit.Exact:
			switch {
			case exact == 0:
				t.Fatalf("Apply(%q, %q, %q, %v) matched text that is not there",
					src, old, new, replaceAll)
			case exact > 1 && !replaceAll:
				t.Fatalf("Apply(%q, %q, %q, %v) replaced %d occurrences without replace_all",
					src, old, new, replaceAll, exact)
			case rep.Count != exact:
				t.Fatalf("Apply(%q, %q, %q, %v) reported %d occurrences, src holds %d",
					src, old, new, replaceAll, rep.Count, exact)
			}
			if want := strings.Join(strings.Split(src, old), new); rep.Text != want {
				t.Fatalf("Apply(%q, %q, %q, %v) = %q, want %q",
					src, old, new, replaceAll, rep.Text, want)
			}

		case edit.Normalized:
			switch {
			case exact != 0:
				t.Fatalf("Apply(%q, %q, %q, %v) took the normalized rung with %d exact matches there",
					src, old, new, replaceAll, exact)
			case rep.Count != len(aligned):
				t.Fatalf("Apply(%q, %q, %q, %v) replaced %d whole-line matches; %d are there at %v",
					src, old, new, replaceAll, rep.Count, len(aligned), aligned)
			case rep.Count > 1 && !replaceAll:
				t.Fatalf("Apply(%q, %q, %q, %v) replaced %d occurrences without replace_all",
					src, old, new, replaceAll, rep.Count)
			}

			// Everything outside the outermost match has to arrive byte for byte:
			// this rung finds whole lines, so a splice off by a line would rewrite
			// one the model never named.
			lines := rawLines(src)
			n := len(trimmedLines(old))
			prefix := strings.Join(lines[:aligned[0]], "")
			suffix := strings.Join(lines[aligned[len(aligned)-1]+n:], "")
			if !strings.HasPrefix(rep.Text, prefix) || !strings.HasSuffix(rep.Text, suffix) {
				t.Fatalf("Apply(%q, %q, %q, %v) = %q, which does not keep %q ahead of the first "+
					"match and %q after the last", src, old, new, replaceAll, rep.Text, prefix, suffix)
			}

			// And re-indentation may move a line, never rewrite one.
			if len(aligned) == 1 {
				spliced := strings.TrimSuffix(strings.TrimPrefix(rep.Text, prefix), suffix)
				if got, want := contentLines(spliced), contentLines(new); !slices.Equal(got, want) {
					t.Fatalf("Apply(%q, %q, %q, %v) spliced in %q, whose lines read %v rather than "+
						"new's %v", src, old, new, replaceAll, spliced, got, want)
				}
			}

		default:
			t.Fatalf("Apply(%q, %q, %q, %v) matched on rung %d, which is not a rung that places text",
				src, old, new, replaceAll, rep.Rung)
		}
	})
}

// alignedOracle answers the normalized rung by a different route: the file's
// trimmed lines joined back into one string and searched for old's, so a match
// is an occurrence at a line boundary rather than a window the scan agreed with
// itself about.
func alignedOracle(src, old string) []int {
	srcLines, oldLines := trimmedLines(src), trimmedLines(old)
	if len(oldLines) == 0 || len(oldLines) > len(srcLines) {
		return nil
	}
	if !slices.ContainsFunc(oldLines, func(s string) bool { return s != "" }) {
		return nil
	}

	// Every line carries its terminator here, including the last, so an
	// occurrence of needle is line-aligned at its end by construction.
	haystack := strings.Join(srcLines, "\n") + "\n"
	needle := strings.Join(oldLines, "\n") + "\n"

	var starts []int
	for from := 0; from+len(needle) <= len(haystack); {
		k := strings.Index(haystack[from:], needle)
		if k < 0 {
			break
		}
		at := from + k
		if at > 0 && haystack[at-1] != '\n' {
			from = at + 1
			continue
		}
		starts = append(starts, strings.Count(haystack[:at], "\n"))
		from = at + len(needle)
	}
	return starts
}

// rawLines and trimmedLines split text the way the ladder does — no trailing
// empty line for a file that ends in a newline — by a route of their own.
func rawLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func trimmedLines(s string) []string {
	lines := rawLines(s)
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return lines
}

// contentLines is trimmedLines with the empty tail dropped, which is what lets a
// replacement be compared against what was spliced in: old_string ending
// mid-line leaves the file's own terminator behind it, exactly as strings.Replace
// would.
func contentLines(s string) []string {
	lines := trimmedLines(s)
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
