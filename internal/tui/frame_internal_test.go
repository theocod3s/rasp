package tui

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tui/chat"
)

// TestTheFirstFrameFillsTheTerminal: the banner on the top row, the footer on
// the bottom one, and the frame exactly as tall as the terminal in between —
// which is what pushes the shell prompt that started the session into
// scrollback (model.go gap).
func TestTheFirstFrameFillsTheTerminal(t *testing.T) {
	tm := teatest.NewTestModel(t, opened(t),
		teatest.WithInitialTermSize(goldenWidth, goldenHeight),
		teatest.WithProgramOptions(tea.WithoutRenderer()),
	)

	frame := quit(t, tm).View().Content
	lines := strings.Split(frame, "\n")
	switch {
	case len(lines) < goldenHeight:
		t.Fatalf("the first frame is %d lines in a terminal %d high, so it leaves dead space under "+
			"it:\n%s", len(lines), goldenHeight, frame)
	case len(lines) > goldenHeight:
		t.Fatalf("the first frame is %d lines in a terminal %d high, so it runs off the screen and "+
			"the top of it is lost:\n%s", len(lines), goldenHeight, frame)
	}
	if strings.TrimSpace(lines[0]) == "" {
		t.Errorf("the frame opens on a blank line, so the banner is not against the top edge:\n%s", frame)
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, string(permission.ModeManual)) {
		t.Errorf("the last line of the frame is %q, and the footer is what has to end it against "+
			"the bottom edge", last)
	}
}

// TestThePaddingShrinksAsTheConversationGrowsAndThenStops. Every line the
// conversation gains is a line of padding it takes back, and past a screenful
// there is none: the frame is then what it would be with no height to pad to,
// byte for byte.
func TestThePaddingShrinksAsTheConversationGrowsAndThenStops(t *testing.T) {
	m := update(idleModel(t), tea.WindowSizeMsg{Width: goldenWidth, Height: goldenHeight})

	var (
		last  = goldenHeight + 1
		short int
		full  int
	)
	for i := range 40 {
		m.chat.Set(strconv.Itoa(i), chat.Notice{Text: "line " + strconv.Itoa(i)})

		with, without := m.View().Content, unpadded(m)
		_, n := padRun(t, with, without)
		if n > last {
			t.Fatalf("the frame gained %d pad line(s) at item %d, having had %d: the padding grew "+
				"as the conversation did", n, i, last)
		}
		last = n

		bare := len(strings.Split(without, "\n"))
		switch {
		case n > 0:
			short++
			if bare >= goldenHeight {
				t.Fatalf("a frame already %d lines long in a terminal %d high was padded with %d "+
					"more:\n%s", bare, goldenHeight, n, with)
			}
			if got := len(strings.Split(with, "\n")); got != goldenHeight {
				t.Fatalf("the padded frame is %d lines in a terminal %d high:\n%s", got, goldenHeight, with)
			}
		case bare < goldenHeight:
			t.Fatalf("a frame %d lines long was not padded to the terminal's %d:\n%s",
				bare, goldenHeight, with)
		default:
			full++
		}
	}

	if short == 0 {
		t.Error("no frame in the walk was ever padded, so nothing above compared a padded frame " +
			"with anything")
	}
	if full == 0 {
		t.Error("the conversation never grew past a screenful, so nothing above pinned the frame " +
			"a full screen leaves alone")
	}
}

// TestATerminalOfUnknownSizeDrawsNoPadding. A height to pad to is something the
// terminal reports, and the frames before the first tea.WindowSizeMsg have none
// — the silence the rules and the hints already keep on an unknown width
// (input.go). Nothing is set by hand here: the size arrives as one message, so
// a model that has not had it has no width either.
func TestATerminalOfUnknownSizeDrawsNoPadding(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})

	before := m.View().Content
	if n := len(strings.Split(before, "\n")); n >= goldenHeight {
		t.Fatalf("a session that has drawn nothing is already %d lines, so a frame of %d proves "+
			"nothing below:\n%s", n, goldenHeight, before)
	}

	m = update(m, tea.WindowSizeMsg{Width: goldenWidth, Height: goldenHeight})

	after := m.View().Content
	if n := len(strings.Split(after, "\n")); n != goldenHeight {
		t.Errorf("the frame is %d lines once the terminal has reported %d:\n%s", n, goldenHeight, after)
	}
	// Against the same model with the height taken back out rather than against
	// the frame above, which was drawn at another width and differs on every line.
	padRun(t, after, unpadded(m))
}

// TestAResizeRePadsTheFrameBothWays. The height arrives again on every resize:
// a window dragged taller has more to fill and one dragged shorter has less,
// down to one too short for the chrome alone, where the frame is left running
// off the screen rather than cut to fit.
func TestAResizeRePadsTheFrameBothWays(t *testing.T) {
	m := update(idleModel(t), tea.WindowSizeMsg{Width: goldenWidth, Height: goldenHeight})
	for i := range 3 {
		m.chat.Set(strconv.Itoa(i), chat.Notice{Text: "line " + strconv.Itoa(i)})
	}

	var grew, shrank, overflowed bool
	height := goldenHeight
	for _, next := range []int{40, 12, 24, 3} {
		m = update(m, tea.WindowSizeMsg{Width: goldenWidth, Height: next})
		grew, shrank = grew || next > height, shrank || next < height
		height = next

		with, without := m.View().Content, unpadded(m)
		padRun(t, with, without)

		drawn := len(strings.Split(with, "\n"))
		if bare := len(strings.Split(without, "\n")); bare >= next {
			overflowed = true
			if drawn != bare {
				t.Errorf("a terminal %d high drew %d lines of a frame that is %d without padding, "+
					"so something was cut to fit:\n%s", next, drawn, bare, with)
			}
			continue
		}
		if drawn != next {
			t.Errorf("the frame is %d lines after a resize to %d:\n%s", drawn, next, with)
		}
	}

	switch {
	case !grew || !shrank:
		t.Error("the walk never both grew and shrank the terminal")
	case !overflowed:
		t.Error("the terminal was never made too short for the frame, so nothing above is about a " +
			"frame that runs off the screen")
	}
}

// TestThePaddingOpensAboveTheWholeBottomBlock. The pad is the last blank line in
// the frame: everything under it — the activity line, the ctrl+c hint, the
// queued messages inside the input frame, the footer — is one block held against
// the bottom edge, and a pad opening anywhere inside it would split the chrome
// or push its lower half off the screen.
func TestThePaddingOpensAboveTheWholeBottomBlock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(*testing.T) Model
		want  string
	}{
		{name: "a turn is running", build: busyModel, want: hintInterrupt},
		{
			name: "ctrl+c is armed",
			build: func(t *testing.T) Model {
				m, _ := idleModel(t).ctrlC()
				return m
			},
			want: hintQuit,
		},
		{
			name: "a message is queued behind the turn",
			build: func(t *testing.T) Model {
				m := busyModel(t)
				m.queue = []string{"also update the README"}
				return m
			},
			want: queueHeader(1),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.build(t)
			m.height = goldenHeight

			frame := m.View().Content
			at, n := padRun(t, frame, unpadded(m))
			if n == 0 {
				t.Fatalf("the frame was not padded, so there is no pad here to be above anything:\n%s", frame)
			}

			lines := strings.Split(frame, "\n")
			if at+n >= len(lines) {
				t.Fatalf("the pad is the end of the frame, so the chrome is above it:\n%s", frame)
			}
			// The line the pad runs into, and only that one: a chrome that grew a
			// blank line of its own would be a change to the chrome, not a pad in
			// the wrong place, and this is about where the pad ends.
			if strings.TrimSpace(lines[at+n]) == "" {
				t.Errorf("line %d is blank and the pad ended at %d, so the pad does not end where "+
					"the block against the bottom edge begins:\n%s", at+n, at+n, frame)
			}
			if !strings.Contains(words(strings.Join(lines[at+n:], "\n")), tc.want) {
				t.Errorf("nothing under the pad says %q, so the state is not the one being drawn:\n%s",
					tc.want, frame)
			}
		})
	}
}

// TestAStandingQuestionStaysWithTheConversation. The permission question is set
// into the conversation where it was asked (prompt.go) rather than drawn as
// chrome, which is the whole of what makes it inline — so the pad opens under
// it, as it does under everything else the transcript holds.
func TestAStandingQuestionStaysWithTheConversation(t *testing.T) {
	m := idleModel(t)
	m.height = goldenHeight
	m.permissions = &answers{decided: true}
	m, _ = m.ask(permission.Request{
		CallID: "call_1",
		Tool:   "edit",
		Action: permission.ActionEdit,
		Path:   "auth.go",
	})

	frame := m.View().Content
	at, n := padRun(t, frame, unpadded(m))
	if n == 0 {
		t.Fatalf("the frame was not padded, so there is nothing here about where the pad opened:\n%s", frame)
	}

	asked := words(strings.Join(strings.Split(frame, "\n")[:at], "\n"))
	if !strings.Contains(asked, "needs your approval") {
		t.Errorf("the question is not above the pad, so it was drawn as chrome rather than as part "+
			"of the conversation:\n%s", frame)
	}
}

// TestTheLastFrameGivesTheScreenBack. Bubble Tea renders the model once more
// after Update has returned tea.Quit and leaves that frame on the screen, so a
// session quit before it filled one would hand the shell back a screenful of
// blank rows under a dead input frame — the same dead space the padding exists
// to remove, after the session rather than before it.
//
// Driven through a real program because the flag has to survive the route
// Update takes to tea.Quit, and read off the model the program ended on, which
// is the one that final render draws.
func TestTheLastFrameGivesTheScreenBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []tea.Msg
	}{
		{name: "ctrl+c twice", keys: []tea.Msg{ctrlCKey, ctrlCKey}},
		{name: "the quit command", keys: typedLine("/quit")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tm := teatest.NewTestModel(t, opened(t),
				teatest.WithInitialTermSize(goldenWidth, goldenHeight),
				teatest.WithProgramOptions(tea.WithoutRenderer()),
			)
			for _, key := range tc.keys {
				tm.Send(key)
			}

			last := quit(t, tm)
			frame := last.View().Content
			if n := len(strings.Split(frame, "\n")); n >= goldenHeight {
				t.Fatalf("the last frame is %d lines in a terminal %d high, so the session was "+
					"never short of a screenful and this proves nothing:\n%s", n, goldenHeight, frame)
			}
			if _, n := padRun(t, frame, unpadded(last)); n != 0 {
				t.Errorf("the frame left on the screen carries %d blank line(s) of padding, which "+
					"is where the shell prompt comes back:\n%s", n, frame)
			}
		})
	}
}

// TestTheLastFrameIsRepaintedOnTheWayOut is the half the test above cannot see.
// It reads the model the program ended on, where what a user is left looking at
// is a render Bubble Tea does after the event loop has stopped (tea.go) — drop
// that render in a future version of the SDK and the flag would go on being set,
// the frame above would go on being short, and the screen would stay full of
// blank rows. So the renderer is left on here and the bytes are read back.
//
// The invitation stands in for the whole frame: a repaint redraws every line,
// where the arming press before it changes one.
func TestTheLastFrameIsRepaintedOnTheWayOut(t *testing.T) {
	const invitation = "type /help for slash commands"

	for _, tc := range []struct {
		name     string
		stop     func(*testing.T, *teatest.TestModel)
		repaints bool
	}{
		{
			name: "quit from the keyboard",
			stop: func(_ *testing.T, tm *teatest.TestModel) {
				tm.Send(ctrlCKey)
				tm.Send(ctrlCKey)
			},
			repaints: true,
		},
		{
			// The control, and the reason the case above says anything: a program
			// stopped from outside never reaches Update (harness_internal_test.go
			// quit), so its last frame is the one already on the screen and Bubble
			// Tea has nothing to paint.
			name: "stopped from outside",
			stop: func(t *testing.T, tm *teatest.TestModel) {
				if err := tm.Quit(); err != nil {
					t.Fatalf("quitting the program: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tm := teatest.NewTestModel(t, opened(t),
				teatest.WithInitialTermSize(goldenWidth, goldenHeight))

			// Drains the output as far as the first frame, so what is read below is
			// only what the way out drew. Waiting on it also says the frame reached
			// the screen at all, which nothing else here would notice.
			teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
				return bytes.Contains(b, []byte(invitation))
			}, teatest.WithDuration(settle))

			tc.stop(t, tm)
			tm.WaitFinished(t, teatest.WithFinalTimeout(settle))

			rest, err := io.ReadAll(tm.Output())
			if err != nil {
				t.Fatalf("reading what the program drew on the way out: %v", err)
			}
			if got := bytes.Contains(rest, []byte(invitation)); got != tc.repaints {
				t.Errorf("the frame was repainted on the way out: %t, want %t — %d bytes drawn "+
					"after the first frame:\n%q", got, tc.repaints, len(rest), rest)
			}
		})
	}
}

// TestEveryQuitGoesThroughQuitting reads the package's own source, because the
// thing that has to hold is about every route out and not about any one of them:
// a tea.Quit returned from somewhere new — an alias for /quit, a dialog that
// leaves, a fatal error — would put the padding back on the frame the session
// leaves behind, and every test here would still pass. Only quitting() may name
// it (model.go).
func TestEveryQuitGoesThroughQuitting(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(f os.FileInfo) bool {
		return !strings.HasSuffix(f.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing the package: %v", err)
	}

	var found int
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				ast.Inspect(fn, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Quit" {
						return true
					}
					if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "tea" {
						return true
					}
					found++
					if fn.Name.Name != "quitting" {
						t.Errorf("%s names tea.Quit at %s; the session's last frame is drawn by a "+
							"model that was told it is leaving, so quitting() is the one place that "+
							"may end a session", fn.Name.Name, fset.Position(sel.Pos()))
					}
					return true
				})
			}
		}
	}

	// Nothing matching is the failure this is most likely to have: a rename, an
	// import under another name, a move out of this directory — each of which
	// leaves the walk above examining nothing and reporting a pass.
	if found == 0 {
		t.Fatal("nothing in this package names tea.Quit, so this examined nothing; the session " +
			"still has to end somewhere, and this check has stopped being able to see where")
	}
}

// padLines is how many lines of padding frame carries, and fails unless it is
// the same frame unpadded plus that run of blank lines — which is what says a
// regenerated golden gained blank lines and nothing else.
//
// Zero has to mean the state fills the screen on its own, so it is checked
// rather than returned: a frame the padding failed to touch is zero too, and a
// caller counting them as the same thing would let a short golden be recorded
// as a full one and then hold it there for good.
func padLines(t *testing.T, m Model, frame string) int {
	t.Helper()

	at, n := padRun(t, frame, unpadded(m))
	lines := strings.Split(frame, "\n")
	if n == 0 {
		if len(lines) < goldenHeight {
			t.Errorf("the frame is %d lines in a terminal %d high and carries no padding at all:\n%s",
				len(lines), goldenHeight, frame)
		}
		return 0
	}
	if at+n >= len(lines) || strings.TrimSpace(lines[at+n]) == "" {
		t.Errorf("the pad does not end where the chrome begins:\n%s", frame)
	}
	if len(lines) != goldenHeight {
		t.Errorf("the frame is %d lines in a terminal %d high:\n%s", len(lines), goldenHeight, frame)
	}
	return n
}

// padRun is where the padding opens in frame and how many lines it takes, given
// the same frame drawn with no height to pad to. It fails unless the two are one
// run of blank lines apart: nothing rewritten, nothing moved, and nothing
// dropped to make the frame fit.
func padRun(t *testing.T, frame, bare string) (at, n int) {
	t.Helper()

	have, want := strings.Split(frame, "\n"), strings.Split(bare, "\n")
	if n = len(have) - len(want); n < 0 {
		t.Fatalf("the padded frame is %d line(s) shorter than the same frame unpadded, so it was "+
			"trimmed rather than padded:\n%s\nwant it to hold\n%s", -n, frame, bare)
	}

	at = len(want)
	for i := range want {
		if have[i] != want[i] {
			at = i
			break
		}
	}
	for i, line := range have[at : at+n] {
		if line != "" {
			t.Fatalf("line %d of the padded frame reads %q, and padding may only add blank "+
				"lines:\n%s", at+i, line, frame)
		}
	}
	if !slices.Equal(have[at+n:], want[at:]) {
		t.Fatalf("the padded frame and the unpadded one differ below line %d rather than only by "+
			"the blank lines at %d:\n%s\nwant it to hold\n%s", at+n, at, frame, bare)
	}
	return at, n
}

// unpadded is the frame m draws with no height to pad to, which is the frame it
// drew before there was any padding at all.
func unpadded(m Model) string {
	m.height = 0
	return m.View().Content
}

// idleModel is a session that has started and done nothing, with a clock the
// machine does not choose — busyModel's counterpart for the states that are not
// a turn running (activity_internal_test.go).
func idleModel(t *testing.T) Model {
	t.Helper()

	m := newModel(t.Context(), newTurner(nil), Config{Mode: permission.ModeManual})
	m.now = newClock(goldenNow).read
	m.width = goldenWidth
	return m
}
