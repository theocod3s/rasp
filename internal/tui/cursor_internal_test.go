package tui

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tui/chat"
)

// blinkingBlock is what tea.NewCursor's defaults encode — shape CursorBlock and
// Blink true — as DECSCUSR spells it. The steady block is the next value up, so
// the number is the whole of the claim that what is on the screen blinks.
const blinkingBlock = 1

// TestTheCursorIsTheTerminalsOwnAndBlinks drives a real cursedRenderer, which
// the golden suite disables (harness_internal_test.go). It has to: the cursor
// never enters the frame, so the frame's own bytes say nothing about it, and
// the only evidence is what Bubble Tea writes to the terminal.
//
// Both halves are here because the sequences mean something only as a pair —
// the cursor is enabled and styled to blink while the input has the keyboard,
// and the style goes back to the terminal's default the moment the completion
// menu takes it. Nothing else in a session writes that second sequence.
func TestTheCursorIsTheTerminalsOwnAndBlinks(t *testing.T) {
	var (
		shown   = []byte(ansi.SetModeTextCursorEnable)
		hidden  = []byte(ansi.ResetModeTextCursorEnable)
		blinks  = []byte(ansi.SetCursorStyle(blinkingBlock))
		dropped = []byte(ansi.SetCursorStyle(0))
	)

	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(goldenWidth, goldenHeight))

	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, shown) && bytes.Contains(b, blinks)
	}, teatest.WithDuration(settle))

	tm.Type("/")
	// The last visibility sequence rather than any of them: an ordinary redraw
	// hides the cursor and shows it again around the update, so only the order
	// says which state the frame ended in.
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, dropped) && bytes.LastIndex(b, hidden) > bytes.LastIndex(b, shown)
	}, teatest.WithDuration(settle))

	if err := tm.Quit(); err != nil {
		t.Fatalf("quitting the program: %v", err)
	}
	tm.WaitFinished(t, teatest.WithFinalTimeout(settle))
}

// TestTheCursorStandsOnTheCellTheCaretIsIn walks the places in a draft the
// cursor has to land on, at both ends of the padding: a frame padded out to the
// terminal and the same frame with no height to pad to (frame.go gap) put the
// input line different distances from the top of the screen and the same
// distance from the bottom.
//
// The column is asserted in cells, and then the cell itself is read back off
// the row that was drawn — which is what makes the wide-rune cases say
// something. A column counted in runes agrees with a cell count everywhere else
// in the table.
func TestTheCursorStandsOnTheCellTheCaretIsIn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		at    int
		yolo  bool
		width int // the terminal, where the default one is too wide to say anything

		col  int    // the cell the cursor stands in
		line int    // which line of the draft that cell is on
		cell string // and what is drawn there
	}{
		{name: "an empty line, on the head of the invitation", col: 2, cell: "a"},
		{name: "the front of a line", text: "fix the auth test", at: 0, col: 2, cell: "f"},
		{name: "part way along a line", text: "fix the auth test", at: 4, col: 6, cell: "t"},
		// Past the last character rather than on it. The terminal's cursor has a
		// cell of its own there; the painted one it replaced had to be given a
		// space to stand on, which is why the draft is drawn plain now (input.go).
		{name: "the end of the draft", text: "fix", at: 3, col: 5, cell: " "},
		{name: "the head of a continuation line", text: "one\ntwo", at: 4, col: 2, line: 1, cell: "t"},
		// Nothing at all under it: the hint is drawn on the draft's last line
		// (input.go hinted), so the line above ends where its text does.
		{name: "the end of a line above the last", text: "one\ntwo", at: 3, col: 5, cell: ""},
		{name: "on a double-width rune", text: "你好", at: len("你"), col: 4, cell: "好"},
		{name: "after two of them", text: "你好x", at: len("你好"), col: 6, cell: "x"},
		{name: "after an emoji", text: "👍ok", at: len("👍"), col: 4, cell: "o"},
		// The yolo caret is two cells wider than the ordinary one and opens the
		// line rather than replacing it (yolo.go), so the invitation starts there.
		{name: "behind the yolo caret", yolo: true, col: 4, cell: "a"},
		// A draft is never cut to the terminal (input.go), so the caret can be
		// sixty columns into a screen twenty wide. The cursor holds the last
		// column there is rather than being placed off the edge.
		{
			name: "a line longer than the terminal", width: 20,
			text: strings.Repeat("0123456789", 6), at: 60, col: 19, cell: "7",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, padding := range []struct {
				name   string
				height int
			}{
				{name: "padded to the terminal", height: goldenHeight},
				{name: "with no height to pad to", height: 0},
			} {
				t.Run(padding.name, func(t *testing.T) {
					m := idleModel(t)
					m.height = padding.height
					m.status.yolo = tc.yolo
					m.input = draft{text: tc.text, at: tc.at}
					if tc.width > 0 {
						m.width = tc.width
					}

					v := m.View()
					if v.Cursor == nil {
						t.Fatalf("the frame carries no cursor at all:\n%s", v.Content)
					}

					lines := strings.Split(ansi.Strip(v.Content), "\n")
					if v.Cursor.Y < 0 || v.Cursor.Y >= len(lines) {
						t.Fatalf("the cursor is on row %d of a frame %d rows tall:\n%s",
							v.Cursor.Y, len(lines), v.Content)
					}
					if want := caretLine(t, lines) + tc.line; v.Cursor.Y != want {
						t.Errorf("the cursor is on row %d, want %d — line %d of the draft:\n%s",
							v.Cursor.Y, want, tc.line, v.Content)
					}
					if v.Cursor.X != tc.col {
						t.Errorf("the cursor is in column %d, want %d — the row reads %q",
							v.Cursor.X, tc.col, lines[v.Cursor.Y])
					}
					if got := cellAt(lines[v.Cursor.Y], v.Cursor.X); got != tc.cell {
						t.Errorf("the cursor stands on %q, want %q — the row reads %q",
							got, tc.cell, lines[v.Cursor.Y])
					}
				})
			}
		})
	}
}

// TestTheCursorHoldsTheInputLineThroughEveryChromeState. How far up from the
// bottom edge the cursor sits is the whole of where it goes (cursor.go), so
// every row that can open or close below the line being typed on moves it —
// and a row that opens above it must not.
func TestTheCursorHoldsTheInputLineThroughEveryChromeState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(*testing.T) Model
	}{
		{
			name:  "an idle session",
			build: func(t *testing.T) Model { return typed(idleModel(t), "fix") },
		}, {
			// Two rows between the draft and the frame's lower rule (queue.go).
			name: "messages queued under the draft",
			build: func(t *testing.T) Model {
				m := typed(idleModel(t), "fix")
				m.queue = []string{"also update the README", "and re-run the failing test"}
				return m
			},
		}, {
			// The control: the activity line opens above the input frame rather
			// than under it (frame.go chrome), so it must move nothing.
			name:  "a turn running above it",
			build: func(t *testing.T) Model { return typed(idleModel(t), "fix").busied() },
		}, {
			// No width to draw a rule across, so both edges of the input frame are
			// gone and the footer is all that is left below the draft (input.go).
			name: "a terminal that has not reported its size",
			build: func(t *testing.T) Model {
				m := idleModel(t)
				m.width = 0
				return typed(m, "fix")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.build(t).View()
			if v.Cursor == nil {
				t.Fatalf("the frame carries no cursor:\n%s", v.Content)
			}

			lines := strings.Split(ansi.Strip(v.Content), "\n")
			if want := caretLine(t, lines); v.Cursor.Y != want {
				t.Errorf("the cursor is on row %d of %d, and the line being typed on is row %d:\n%s",
					v.Cursor.Y, len(lines), want, v.Content)
			}
		})
	}
}

// TestTheCursorIsHiddenWhenTheInputIsNotWhereTheNextKeystrokeLands. A cursor is
// a promise about the next key, so it goes wherever that promise is false. The
// first case is the control: without it a cursor that had stopped being drawn at
// all would satisfy every other case here.
func TestTheCursorIsHiddenWhenTheInputIsNotWhereTheNextKeystrokeLands(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(*testing.T) Model
		drawn bool
	}{
		{
			name:  "the input has the keyboard",
			build: func(t *testing.T) Model { return typed(idleModel(t), "fix") },
			drawn: true,
		}, {
			name: "a question stands",
			build: func(t *testing.T) Model {
				m := idleModel(t)
				m.permissions = &answers{decided: true}
				m, _ = m.ask(permission.Request{
					CallID: "call_1",
					Tool:   "edit",
					Action: permission.ActionEdit,
					Path:   "auth.go",
				})
				return m
			},
		}, {
			name:  "the completion menu is open",
			build: func(t *testing.T) Model { return typed(idleModel(t), "/") },
		}, {
			name: "the session is leaving",
			build: func(t *testing.T) Model {
				m, _ := typed(idleModel(t), "fix").quitting()
				return m
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.build(t)
			m.height = goldenHeight

			if drawn := m.View().Cursor != nil; drawn != tc.drawn {
				t.Errorf("a cursor is drawn: %t, want %t:\n%s", drawn, tc.drawn, m.View().Content)
			}
		})
	}
}

// TestMovingTheCaretMovesNothingButTheCursor is three claims in one comparison.
// The caret is no longer painted into the input line, so moving it along a
// draft leaves the frame byte for byte what it was; the cursor travels on
// tea.View rather than inside Content, so the recorded goldens cannot move with
// it (the same claim terminal.go makes for the window title); and the cursor did
// move, so neither of the first two is true merely because nothing happened.
func TestMovingTheCaretMovesNothingButTheCursor(t *testing.T) {
	const back = 4

	m := idleModel(t)
	m.height = goldenHeight
	m = typed(m, "fix the auth test")

	end := m.View()
	for range back {
		m, _ = m.press(key(tea.KeyLeft))
	}
	moved := m.View()

	if end.Cursor == nil || moved.Cursor == nil {
		t.Fatalf("the input frame carries no cursor, so there is nothing here to have moved:\n%s", end.Content)
	}
	if want := end.Cursor.X - back; moved.Cursor.X != want {
		t.Errorf("the cursor is in column %d after %d presses of left, want %d",
			moved.Cursor.X, back, want)
	}
	if moved.Content != end.Content {
		t.Errorf("moving the caret redrew the frame, so something in it is still following the "+
			"caret:\n%s\nwas\n%s", moved.Content, end.Content)
	}
}

// TestTheInputLinePaintsNoCaretCell is the other half of the frame comparison
// above, said about the one cell rather than about the whole line: the draft is
// drawn plain now, and inverse video is left to the yolo badge and its rules,
// which are still drawn in it (status.go).
func TestTheInputLinePaintsNoCaretCell(t *testing.T) {
	m := idleModel(t)
	// Over a character rather than past the last one, where the cell a painted
	// caret took was the space it had to be given to stand on — a space in
	// inverse video is far easier to draw by accident than a letter is.
	m.input = draft{text: "fix the auth test"}

	// What the painted caret used to put in the frame, built the way it was
	// built: the cell under the caret, in inverse video.
	painted := lipgloss.NewStyle().Reverse(true).Render("f")
	if !strings.Contains(painted, "\x1b[") {
		t.Fatal("lipgloss renders without escape sequences here, so the inverse cell below is the " +
			"bare letter and every draft holding one would fail this")
	}
	if strings.Contains(m.typing(), painted) {
		t.Errorf("the input line still paints its caret cell, and a painted cell cannot blink: %q",
			m.typing())
	}
}

// TestTheCursorHoldsTheInputLineOnAFrameTallerThanTheScreen. Past a screenful
// nothing is padded and nothing is trimmed (frame.go gap), and Bubble Tea's own
// renderer drops the rows that overflow from the top of the frame before it
// places the cursor — so a row counted down from the top of the frame would put
// the cursor that many lines below the terminal's last one.
func TestTheCursorHoldsTheInputLineOnAFrameTallerThanTheScreen(t *testing.T) {
	m := idleModel(t)
	m.height = goldenHeight
	for i := range 40 {
		m.chat.Set(strconv.Itoa(i), chat.Notice{Text: "line " + strconv.Itoa(i)})
	}
	m = typed(m, "fix")

	v := m.View()
	lines := strings.Split(ansi.Strip(v.Content), "\n")
	if len(lines) <= goldenHeight {
		t.Fatalf("the frame is %d rows in a terminal %d high, so it never overflowed and there is "+
			"nothing here about one that does", len(lines), goldenHeight)
	}
	if v.Cursor == nil {
		t.Fatalf("the frame carries no cursor:\n%s", v.Content)
	}

	// Counted the way the renderer counts once the rows above the screen are
	// gone: up from the bottom of the frame, which is the bottom of the terminal.
	input := caretLine(t, lines)
	if want := goldenHeight - (len(lines) - input); v.Cursor.Y != want {
		t.Errorf("the cursor is on row %d of a terminal %d high, want %d — the input is row %d of a "+
			"frame %d rows tall", v.Cursor.Y, goldenHeight, want, input, len(lines))
	}
}

// cursorCell is what the terminal's cursor stands on in m's frame: the row
// tea.View names, stripped of styling, and the cell at the column it names.
func cursorCell(t *testing.T, m Model) string {
	t.Helper()

	v := m.View()
	if v.Cursor == nil {
		t.Fatalf("the frame carries no cursor:\n%s", v.Content)
	}
	lines := strings.Split(ansi.Strip(v.Content), "\n")
	if v.Cursor.Y < 0 || v.Cursor.Y >= len(lines) {
		t.Fatalf("the cursor is on row %d of a frame %d rows tall:\n%s", v.Cursor.Y, len(lines), v.Content)
	}
	return cellAt(lines[v.Cursor.Y], v.Cursor.X)
}

// cellAt is the character occupying column col of an unstyled line, and "" past
// the end of one. Columns rather than runes, so a double-width rune before col
// is stepped over the way a terminal steps over it.
func cellAt(line string, col int) string {
	rest := ansi.TruncateLeft(line, col, "")
	if rest == "" {
		return ""
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return string(r)
}
