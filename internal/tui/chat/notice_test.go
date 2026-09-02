package chat_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/tui/chat"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// TestANoticeIsDrawnExactlyAsItWasWritten. A notice carries the UI's own words
// — a command name, the backticks and asterisks around one — and the two other
// things this view draws would both change them: a reply goes through the
// markdown renderer, and a prompt is given the caret that says a person typed it.
func TestANoticeIsDrawnExactlyAsItWasWritten(t *testing.T) {
	const text = "There is no /modle command. *`/help`* lists the ones there are."

	drawn := ansi.Strip(chat.Notice{Text: text}.Render(wide))

	if drawn != text {
		t.Errorf("a notice drew\n\t%q\nand was written as\n\t%q", drawn, text)
	}
}

// TestANoticeWrapsToTheTerminal. /help is a block of lines and the answer to a
// command is a sentence or two, so a notice is the one item here most likely to
// be wider than the screen.
func TestANoticeWrapsToTheTerminal(t *testing.T) {
	const narrow = 40

	drawn := ansi.Strip(chat.Notice{Text: strings.Repeat("cleared ", 20)}.Render(narrow))

	if !strings.Contains(drawn, "\n") {
		t.Fatalf("a notice far wider than the terminal came back on one line:\n%s", drawn)
	}
	for _, line := range strings.Split(drawn, "\n") {
		if n := ansi.StringWidth(line); n > narrow {
			t.Errorf("a line runs %d columns into a terminal %d wide: %q", n, narrow, line)
		}
	}
}

// TestANoticeIsDrawnFaint. rasp's own voice is subordinate to the
// conversation's, and a mode reminder three lines long is where that stops
// being a detail — drawn at the weight of a reply it reads as one.
//
// Asserted against the token rather than against "has some colour on it",
// because the regression this pins was not a missing escape: the notice was
// drawn in Muted, which is the shade for information a reader has to take in
// (palette.go), and every test here compared ansi.Strip-ed text and saw
// nothing.
func TestANoticeIsDrawnFaint(t *testing.T) {
	for _, bg := range []struct {
		name string
		bg   styles.Background
	}{
		{"dark", styles.Dark},
		{"light", styles.Light},
	} {
		t.Run(bg.name, func(t *testing.T) {
			const text = "[Mode changed to plan. You can no longer edit or write files.]"

			drawn := chat.Notice{Text: text, Background: bg.bg}.Render(wide)

			if want := styles.For(bg.bg).Faint.Render(text); drawn != want {
				t.Errorf("a notice drew\n\t%q\nand the faint token draws it\n\t%q", drawn, want)
			}
		})
	}
}

// TestAnEmptyNoticeTakesNoLine. The view gives every item that draws something
// a line of its own, so an empty one has to come back empty rather than as a
// blank string with a colour on it.
func TestAnEmptyNoticeTakesNoLine(t *testing.T) {
	if drawn := (chat.Notice{}).Render(wide); drawn != "" {
		t.Errorf("an empty notice drew %q", drawn)
	}
}

// TestAParagraphBreakInANoticeStaysBlank. A notice with two paragraphs — a
// mode reminder, an unknown-command reply with more than one sentence in it —
// is drawn through paint the same way thinking is, and paint's own rule about
// a blank line applies here too: a style's Render("") is not "", so a naive
// per-line style would leave a paragraph break carrying nothing but a dead
// escape sequence rather than staying blank.
func TestAParagraphBreakInANoticeStaysBlank(t *testing.T) {
	drawn := chat.Notice{Text: "first line\n\nthird line"}.Render(wide)

	lines := strings.Split(drawn, "\n")
	if len(lines) != 3 {
		t.Fatalf("the notice drew %d line(s), want the paragraph break as one of them:\n%q", len(lines), lines)
	}
	if lines[1] != "" {
		t.Errorf("the paragraph break reads %q, want a truly blank line", lines[1])
	}
}

// TestAnErrorNoticeIsDrawnInTheErrorToken. A failed turn is the one thing this
// family says that a reader must not be able to skim past, so it takes the
// palette's own accent for "this went wrong" rather than the Faint every other
// notice draws in — asserted against the token on both backgrounds, for the
// reason TestANoticeIsDrawnFaint already is.
func TestAnErrorNoticeIsDrawnInTheErrorToken(t *testing.T) {
	for _, bg := range []struct {
		name string
		bg   styles.Background
	}{
		{"dark", styles.Dark},
		{"light", styles.Light},
	} {
		t.Run(bg.name, func(t *testing.T) {
			const text = "error: the provider closed the stream mid-message"

			drawn := chat.Notice{Text: text, Kind: chat.NoticeError, Background: bg.bg}.Render(wide)

			if want := styles.For(bg.bg).Error.Render(text); drawn != want {
				t.Errorf("an error notice drew\n\t%q\nand the error token draws it\n\t%q", drawn, want)
			}
			if faint := styles.For(bg.bg).Faint.Render(text); drawn == faint {
				t.Errorf("an error notice drew the same bytes an ordinary one would:\n\t%q", drawn)
			}
		})
	}
}
