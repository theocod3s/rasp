package chat_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/tui/chat"
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

// TestAnEmptyNoticeTakesNoLine. The view gives every item that draws something
// a line of its own, so an empty one has to come back empty rather than as a
// blank string with a colour on it.
func TestAnEmptyNoticeTakesNoLine(t *testing.T) {
	if drawn := (chat.Notice{}).Render(wide); drawn != "" {
		t.Errorf("an empty notice drew %q", drawn)
	}
}
