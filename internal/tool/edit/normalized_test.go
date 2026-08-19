package edit_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/tool/edit"
)

// tabbed is the shape the normalized rung exists for: a file indented with tabs,
// which a model that re-typed rather than copied will hand back as spaces.
const tabbed = "func f() {\n\tif x {\n\t\treturn 1\n\t}\n}\n"

func TestNormalizedMatchAcceptsWholeLineDrift(t *testing.T) {
	tests := map[string]struct {
		src, old, new string
		want          string
	}{
		"spaces where the file has tabs": {
			src:  tabbed,
			old:  "    if x {\n        return 1\n    }",
			new:  "    if x {\n        return 2\n    }",
			want: "func f() {\n\tif x {\n\t\treturn 2\n\t}\n}\n",
		},
		"no indentation at all": {
			src:  tabbed,
			old:  "if x {\nreturn 1\n}",
			new:  "if x {\nreturn 2\n}",
			want: "func f() {\n\tif x {\n\t\treturn 2\n\t}\n}\n",
		},
		"deeper than the file": {
			src:  tabbed,
			old:  "\t\t\t\treturn 1",
			new:  "\t\t\t\treturn 2",
			want: "func f() {\n\tif x {\n\t\treturn 2\n\t}\n}\n",
		},
		"trailing whitespace on the line": {
			src:  "\tport := 8080\n",
			old:  "\tport := 8080   ",
			new:  "\tport := 9090",
			want: "\tport := 9090\n",
		},
		"the file's line ending survives": {
			src:  "a\r\nb\r\nc\r\n",
			old:  "  b",
			new:  "B",
			want: "a\r\nB\r\nc\r\n",
		},
		"deleting a line whose indentation drifted": {
			src:  "keep\n\tdrop\nkeep\n",
			old:  "    drop\n",
			new:  "",
			want: "keep\nkeep\n",
		},
		"new_string spans more lines than old_string": {
			src:  tabbed,
			old:  "  return 1",
			new:  "  log(1)\n  return 1",
			want: "func f() {\n\tif x {\n\t\tlog(1)\n\t\treturn 1\n\t}\n}\n",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rep, err := edit.Apply(tc.src, tc.old, tc.new, false)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if rep.Text != tc.want {
				t.Errorf("text = %q, want %q", rep.Text, tc.want)
			}
			if rep.Count != 1 {
				t.Errorf("count = %d, want 1", rep.Count)
			}
			if rep.Rung != edit.Normalized {
				t.Errorf("rung = %d, want %d (Normalized); a caller reads the rung to decide "+
					"whether to tell the model its match was not byte-exact", rep.Rung, edit.Normalized)
			}
		})
	}
}

// TestNormalizedMatchIsWholeLineAligned holds the boundary that keeps the rung
// from being a substring search over trimmed lines. Every case here is a fragment
// the exact rung missed only because whitespace drifted at its ends, and a rung
// that compared by containment rather than equality would splice new_string into
// the middle of a line the model never read.
func TestNormalizedMatchIsWholeLineAligned(t *testing.T) {
	tests := map[string]struct{ src, old, new string }{
		"part of a line": {
			src: "\tport := 8080\n", old: "  := 8080", new: "  := 9090",
		},
		"the head of a line, with the rest left off": {
			src: "\tif err != nil {\n\t\treturn err\n\t}\n",
			old: "  if err != nil\n    return err",
			new: "  if err != nil\n    return nil",
		},
		"the tail of one line and the head of the next": {
			src: "a := 1\nb := 2\n", old: "1  \n  b", new: "1\nc",
		},
		"internal whitespace differs": {
			// Deliberately refused. Two spaces where the file has one is content
			// the model did not read, and this rung is whitespace at line ends.
			src: "\tport :=  8080\n", old: "port := 8080", new: "port := 9090",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rep, err := edit.Apply(tc.src, tc.old, tc.new, false)
			if !errors.Is(err, edit.ErrNotFound) {
				t.Fatalf("Apply(...) error = %v, want ErrNotFound", err)
			}
			if rep != (edit.Replacement{}) {
				t.Errorf("a refused edit produced %+v", rep)
			}
		})
	}
}

// TestNormalizedAmbiguityIsRefused is the ambiguity rule surviving normalization,
// which is where it matters most: normalizing widens what counts as an
// occurrence, so text that was unique byte for byte can stop being unique here.
func TestNormalizedAmbiguityIsRefused(t *testing.T) {
	// Spaces against the file's tabs, so the exact rung counts zero occurrences
	// and the refusal under test is the normalized one. Bare "port := 8080" would
	// be found twice byte for byte, inside the indentation of both lines.
	const src = "func a() {\n\tport := 8080\n}\n\nfunc b() {\n\t\tport := 8080\n}\n"

	rep, err := edit.Apply(src, "    port := 8080", "    port := 9090", false)
	var ambiguous *edit.AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("Apply(...) error = %v, want *edit.AmbiguousError", err)
	}
	if ambiguous.Count != 2 {
		t.Errorf("count = %d, want 2", ambiguous.Count)
	}
	if rep != (edit.Replacement{}) {
		t.Errorf("a refused edit produced %+v", rep)
	}

	// With replace_all, each occurrence is re-indented to its own location rather
	// than to the first one found.
	rep, err = edit.Apply(src, "    port := 8080", "    port := 9090", true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.Rung != edit.Normalized {
		t.Fatalf("rung = %d, want %d (Normalized); this test only says anything about the "+
			"normalized rung's ambiguity check if that is the rung it reached", rep.Rung, edit.Normalized)
	}
	want := "func a() {\n\tport := 9090\n}\n\nfunc b() {\n\t\tport := 9090\n}\n"
	if rep.Text != want {
		t.Errorf("text = %q, want %q", rep.Text, want)
	}
	if rep.Count != 2 {
		t.Errorf("count = %d, want 2", rep.Count)
	}
}

func TestReindentationFollowsTheFilesUnit(t *testing.T) {
	tests := map[string]struct {
		src, old, new string
		want          string
	}{
		"tabs from a four-space replacement": {
			src:  "\tfunc f() {\n\t\tbody()\n\t}\n",
			old:  "    func f() {\n        body()\n    }",
			new:  "    func f() {\n        first()\n        second()\n    }",
			want: "\tfunc f() {\n\t\tfirst()\n\t\tsecond()\n\t}\n",
		},
		"two spaces from a tabbed replacement": {
			src:  "a:\n  b:\n    c: 1\n",
			old:  "\tb:\n\t\tc: 1",
			new:  "\tb:\n\t\tc: 2\n\t\td: 3",
			want: "a:\n  b:\n    c: 2\n    d: 3\n",
		},
		"a line shallower than the matched one": {
			src:  "func f() {\n\t\tif x {\n\t\t\treturn 1\n\t\t}\n}\n",
			old:  "  if x {\n    return 1\n  }",
			new:  "if x {\n  return 1\n}",
			want: "func f() {\n\tif x {\n\t\treturn 1\n\t}\n}\n",
		},
		"a blank line in the replacement gets no indentation": {
			src:  "\tone\n",
			old:  "  one",
			new:  "  one\n   \n  two",
			want: "\tone\n\n\ttwo\n",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rep, err := edit.Apply(tc.src, tc.old, tc.new, false)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			// Re-indentation only happens on the normalized rung, so a case the
			// exact rung could answer would pass while proving nothing about it.
			if rep.Rung != edit.Normalized {
				t.Fatalf("rung = %d, want %d (Normalized)", rep.Rung, edit.Normalized)
			}
			if rep.Text != tc.want {
				t.Errorf("text = %q, want %q", rep.Text, tc.want)
			}
		})
	}
}

// TestReindentationLeavesMatchingIndentationAlone is re-indentation's identity
// case, and the one a wrong unit shows up in: when the model's convention is
// already the file's, every byte of new_string has to arrive as written.
func TestReindentationLeavesMatchingIndentationAlone(t *testing.T) {
	const src = "def f():\n    if x:\n        return 1\n"

	// Only the trailing space on the first line is drift, so nothing else may move.
	rep, err := edit.Apply(src, "    if x: \n        return 1", "    if x:\n        return 2", false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.Rung != edit.Normalized {
		t.Fatalf("rung = %d, want %d (Normalized)", rep.Rung, edit.Normalized)
	}
	if want := "def f():\n    if x:\n        return 2\n"; rep.Text != want {
		t.Errorf("text = %q, want %q", rep.Text, want)
	}
}

// TestNormalizedNoOpIsRefused catches the edit that reports a replacement and
// changes nothing: old_string and new_string differ, but only in the whitespace
// this rung ignores, so re-indenting lands back on what the file already holds.
func TestNormalizedNoOpIsRefused(t *testing.T) {
	rep, err := edit.Apply("\tx := 1\n", "x := 1  ", "x := 1", false)
	if !errors.Is(err, edit.ErrUnchanged) {
		t.Fatalf("Apply(...) error = %v, want ErrUnchanged", err)
	}
	if rep != (edit.Replacement{}) {
		t.Errorf("a refused edit produced %+v", rep)
	}
}

// TestWhitespaceOnlyOldStringMatchesNothing is ErrEmpty's hazard wearing
// whitespace: old_string that trims to nothing sits at every blank line in the
// file, so a rung that accepted it would be choosing a position rather than
// finding one.
func TestWhitespaceOnlyOldStringMatchesNothing(t *testing.T) {
	rep, err := edit.Apply("a\n\nb\n\nc\n", "   \n", "inserted\n", false)
	if !errors.Is(err, edit.ErrNotFound) {
		t.Fatalf("Apply(...) error = %v, want ErrNotFound", err)
	}
	if rep != (edit.Replacement{}) {
		t.Errorf("a refused edit produced %+v", rep)
	}
}

// TestExactMatchOutranksANormalizedOne pins the ladder's order. src holds the
// text byte for byte in one place and with drifted indentation in another; a
// ladder that ran the normalized scan first would call this ambiguous and refuse
// an edit that has one exact answer.
func TestExactMatchOutranksANormalizedOne(t *testing.T) {
	const src = "func a() {\n\treturn 1\n}\n\nfunc b() {\n        return 1\n}\n"

	rep, err := edit.Apply(src, "\treturn 1", "\treturn 2", false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.Rung != edit.Exact {
		t.Errorf("rung = %d, want %d (Exact)", rep.Rung, edit.Exact)
	}
	if want := "func a() {\n\treturn 2\n}\n\nfunc b() {\n        return 1\n}\n"; rep.Text != want {
		t.Errorf("text = %q, want %q", rep.Text, want)
	}
}

// TestNoMatchReportsTheClosestLocation is the rung people skip and the one worth
// the most: "not found" makes the model guess again, while its own file back
// with the whitespace made visible lets it correct its input.
func TestNoMatchReportsTheClosestLocation(t *testing.T) {
	const src = "func f() {\n\tif x {\n\t\treturn 1  \n\t}\n}\n"

	// The model wrote the line it wants rather than the line that is there, so the
	// block matches at its ends and not in the middle.
	rep, err := edit.Apply(src, "\tif x {\n\t\treturn 2\n\t}", "\tif x {\n\t\treturn 3\n\t}", false)
	if rep != (edit.Replacement{}) {
		t.Errorf("a refused edit produced %+v", rep)
	}

	var notFound *edit.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("Apply(...) error = %v, want *edit.NotFoundError", err)
	}
	if !errors.Is(err, edit.ErrNotFound) {
		t.Error("a near miss stopped answering to ErrNotFound, which is what a caller " +
			"that only cares whether the ladder failed asks with")
	}
	if notFound.Line != 2 {
		t.Errorf("line = %d, want 2 (the `if x {` the model's first line matched)", notFound.Line)
	}

	want := "2 | " + edit.TabGlyph + "if x {\n" +
		"3 | " + strings.Repeat(edit.TabGlyph, 2) + "return 1" + strings.Repeat(edit.SpaceGlyph, 2) + "\n" +
		"4 | " + edit.TabGlyph + "}\n"
	if notFound.Actual != want {
		t.Errorf("actual =\n%q\nwant\n%q", notFound.Actual, want)
	}
	if strings.Contains(notFound.Actual, "\t") {
		t.Error("the quoted lines still hold a real tab, so the difference the model has to " +
			"see is as invisible as it was in the file")
	}
}

// TestNoMatchWithoutAnAnchorPointsNowhere is the other half of rung 4: with no
// line in common there is no closest location, and the file's first lines quoted
// anyway would be a guess wearing the clothes of evidence.
func TestNoMatchWithoutAnAnchorPointsNowhere(t *testing.T) {
	_, err := edit.Apply("alpha\nbeta\ngamma\n", "nothing like it", "x", false)

	var notFound *edit.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("Apply(...) error = %v, want *edit.NotFoundError", err)
	}
	if notFound.Line != 0 || notFound.Actual != "" {
		t.Errorf("line = %d, actual = %q; want no location at all", notFound.Line, notFound.Actual)
	}
}

// TestNearMissIsBounded keeps a long old_string from returning the file to the
// model a line at a time.
func TestNearMissIsBounded(t *testing.T) {
	var src, old strings.Builder
	for i := range 200 {
		src.WriteString("line\n")
		if i == 0 {
			old.WriteString("nowhere\n")
			continue
		}
		old.WriteString("line\n")
	}

	_, err := edit.Apply(src.String(), old.String(), "replacement", false)
	var notFound *edit.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("Apply(...) error = %v, want *edit.NotFoundError", err)
	}
	if got := strings.Count(notFound.Actual, "\n"); got > 20 {
		t.Errorf("the near miss quoted %d lines back", got)
	}
	if notFound.Actual == "" {
		t.Error("the near miss quoted nothing, so the bound is being met by having no content")
	}
}
