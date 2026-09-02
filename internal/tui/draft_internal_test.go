package tui

import "testing"

// TestADraftEditsWhereTheCaretIs. Every edit is relative to the caret rather
// than to the end of the text, which is the whole difference between a line that
// can be corrected and one that can only be retyped.
func TestADraftEditsWhereTheCaretIs(t *testing.T) {
	for _, tc := range []struct {
		name string
		from draft
		edit func(draft) draft
		want draft
	}{
		{
			name: "insert splits the text at the caret",
			from: draft{text: "fix the test", at: 4},
			edit: func(d draft) draft { return d.insert("auth ") },
			want: draft{text: "fix auth the test", at: 9},
		}, {
			name: "insert of nothing leaves the draft alone",
			from: draft{text: "fix", at: 1},
			edit: func(d draft) draft { return d.insert("") },
			want: draft{text: "fix", at: 1},
		}, {
			name: "backspace takes the rune before the caret, not the last one",
			from: draft{text: "fix the test", at: 4},
			edit: draft.backspace,
			want: draft{text: "fixthe test", at: 3},
		}, {
			// A caret one byte inside é would slice the rune in half, and the frame
			// would draw the replacement character for the rest of the session.
			name: "backspace takes a whole multibyte rune",
			from: draft{text: "café", at: len("café")},
			edit: draft.backspace,
			want: draft{text: "caf", at: 3},
		}, {
			name: "backspace at the front does nothing",
			from: draft{text: "fix", at: 0},
			edit: draft.backspace,
			want: draft{text: "fix", at: 0},
		}, {
			name: "left steps back over a line break",
			from: draft{text: "one\ntwo", at: 4},
			edit: draft.left,
			want: draft{text: "one\ntwo", at: 3},
		}, {
			name: "right steps forward over a line break",
			from: draft{text: "one\ntwo", at: 3},
			edit: draft.right,
			want: draft{text: "one\ntwo", at: 4},
		}, {
			name: "right at the end does nothing",
			from: draft{text: "one", at: 3},
			edit: draft.right,
			want: draft{text: "one", at: 3},
		}, {
			name: "up holds the column",
			from: draft{text: "abcdef\nghijkl", at: 10}, // column 3 of line two
			edit: draft.up,
			want: draft{text: "abcdef\nghijkl", at: 3},
		}, {
			name: "up onto a shorter line stops at its end",
			from: draft{text: "ab\nghijkl", at: 8}, // column 5 of line two
			edit: draft.up,
			want: draft{text: "ab\nghijkl", at: 2},
		}, {
			name: "up on the first line leaves the caret where it is",
			from: draft{text: "abcdef\nghijkl", at: 3},
			edit: draft.up,
			want: draft{text: "abcdef\nghijkl", at: 3},
		}, {
			name: "down holds the column",
			from: draft{text: "abcdef\nghijkl", at: 3},
			edit: draft.down,
			want: draft{text: "abcdef\nghijkl", at: 10},
		}, {
			name: "down onto a shorter line stops at its end",
			from: draft{text: "abcdef\ngh", at: 5},
			edit: draft.down,
			want: draft{text: "abcdef\ngh", at: 9},
		}, {
			name: "down on the last line leaves the caret where it is",
			from: draft{text: "abcdef\nghijkl", at: 10},
			edit: draft.down,
			want: draft{text: "abcdef\nghijkl", at: 10},
		}, {
			// The column is counted in runes, so a line of two-byte characters above
			// a line of one-byte ones does not land the caret twice as far along.
			name: "up counts the column in runes, not bytes",
			from: draft{text: "ααα\nabcdef", at: len("ααα\n") + 2},
			edit: draft.up,
			want: draft{text: "ααα\nabcdef", at: len("αα")},
		}, {
			name: "home is the caret's own line, not the draft",
			from: draft{text: "one\ntwo", at: 6},
			edit: draft.home,
			want: draft{text: "one\ntwo", at: 4},
		}, {
			name: "end is the caret's own line, not the draft",
			from: draft{text: "one\ntwo", at: 0},
			edit: draft.end,
			want: draft{text: "one\ntwo", at: 3},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.edit(tc.from); got != tc.want {
				t.Errorf("editing %+v gave %+v, want %+v", tc.from, got, tc.want)
			}
		})
	}
}

// TestTheCaretAlwaysHasACellToStandOn. The caret is drawn by inverting one cell
// (input.go), so split has to hand back a rune even where the text has none —
// at the end of a line, and at the end of the draft.
func TestTheCaretAlwaysHasACellToStandOn(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		from                 draft
		before, under, after string
	}{
		{
			name:   "over a rune",
			from:   draft{text: "one\ntwo", at: 4},
			before: "one\n", under: "t", after: "wo",
		}, {
			// The break stays in the tail: it is what ends the line the caret is on,
			// and swallowing it would join two lines into one on the screen.
			name:   "at the end of a line",
			from:   draft{text: "one\ntwo", at: 3},
			before: "one", under: " ", after: "\ntwo",
		}, {
			name:   "at the end of the draft",
			from:   draft{text: "one", at: 3},
			before: "one", under: " ", after: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, under, after := tc.from.split()
			if before != tc.before || under != tc.under || after != tc.after {
				t.Errorf("split gave (%q, %q, %q), want (%q, %q, %q)",
					before, under, after, tc.before, tc.under, tc.after)
			}
		})
	}
}
