package dialog_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tui/dialog"
)

const width = 80

func edit() permission.Request {
	return permission.Request{
		CallID: "call_1",
		Tool:   "edit",
		Action: permission.ActionEdit,
		Path:   "/w/internal/auth/check.go",
	}
}

// TestEveryAnswerHasAKeyAndTheScreenSaysWhichOne is the once/always/reject
// criterion where it can be checked without a terminal: permission takes
// exactly three answers, so a prompt that offers two of them leaves one
// unreachable — and one offered under a key the screen does not name is
// unreachable too, by anyone who has not read the source.
func TestEveryAnswerHasAKeyAndTheScreenSaysWhichOne(t *testing.T) {
	keys := map[permission.Decision]rune{}
	// Every printable key, so a decision reachable by no key at all is caught
	// rather than assumed: asking Decide about the three we expect would agree
	// with a table that had dropped one.
	for r := rune(' '); r <= '~'; r++ {
		decision, ok := dialog.Decide(r)
		if !ok {
			continue
		}
		if first, taken := keys[decision]; taken {
			t.Errorf("%q and %q both answer %q", first, r, decision)
			continue
		}
		keys[decision] = r
	}

	choices := lastLine(t, dialog.Permission{Request: edit()}.Render(width))
	for _, decision := range []permission.Decision{
		permission.DecisionOnce,
		permission.DecisionAlways,
		permission.DecisionReject,
	} {
		key, ok := keys[decision]
		if !ok {
			t.Errorf("no key answers %q, so a reader cannot give that answer", decision)
			continue
		}
		if !strings.Contains(choices, string(key)+" ") {
			t.Errorf("the answers read %q and never name the %q key, which is the one that means %q",
				choices, key, decision)
		}
	}
}

// TestAKeyThatAnswersNothingSaysSo. Decide reporting a decision for a key it
// does not know would make every stray press an answer.
func TestAKeyThatAnswersNothingSaysSo(t *testing.T) {
	for _, key := range []rune{'q', 'z', ' ', '1', 'Y'} {
		if decision, ok := dialog.Decide(key); ok {
			t.Errorf("%q answers %q, and it is not one of the prompt's keys", key, decision)
		}
	}
}

// TestThePromptSaysWhatItIsAskingAbout: the request is the whole of what the
// reader has to go on, so a prompt that draws the tool without the path or the
// command asks them to approve a category rather than an action.
func TestThePromptSaysWhatItIsAskingAbout(t *testing.T) {
	tests := []struct {
		name    string
		request permission.Request
		want    []string
	}{
		{
			name:    "a file the model wants to change",
			request: edit(),
			want:    []string{"edit", "/w/internal/auth/check.go"},
		},
		{
			name: "a command",
			request: permission.Request{
				CallID:  "call_2",
				Tool:    "bash",
				Action:  permission.ActionExecute,
				Command: "go test ./...",
			},
			want: []string{"bash", "go test ./..."},
		},
		{
			// Both, which no builtin produces today and the drawing must not
			// quietly drop half of: a reader approving a command against a path
			// has been shown one of the two things they are agreeing to.
			name: "both",
			request: permission.Request{
				CallID:  "call_3",
				Tool:    "mcp__db__query",
				Action:  permission.ActionExecute,
				Path:    "/w/schema.sql",
				Command: "DROP TABLE users",
			},
			want: []string{"mcp__db__query", "/w/schema.sql", "DROP TABLE users"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			frame := ansi.Strip(dialog.Permission{Request: tc.request}.Render(width))
			for _, want := range tc.want {
				if !strings.Contains(frame, want) {
					t.Errorf("the prompt does not mention %q:\n%s", want, frame)
				}
			}
		})
	}
}

// TestAnAnsweredPromptLeavesTheTranscript. The question is an item like any
// other, so it keeps its place in the conversation; what it must not keep is a
// line on every frame for the rest of the session, since the tool's own card
// says what happened next.
func TestAnAnsweredPromptLeavesTheTranscript(t *testing.T) {
	answered := dialog.Permission{Request: edit(), Answered: true}
	if !answered.Finished() {
		t.Error("an answered prompt is not finished, so the view redraws it on every frame")
	}
	if got := answered.Render(width); got != "" {
		t.Errorf("an answered prompt still draws %q", got)
	}
	if open := (dialog.Permission{Request: edit()}); open.Finished() {
		t.Error("an open prompt reports itself finished, so the view would freeze the first frame of it")
	}
}

// TestThePromptFitsTheTerminal: it is drawn inline, above the input, so a line
// running past the edge wraps into the line below and takes the answers with it.
func TestThePromptFitsTheTerminal(t *testing.T) {
	long := permission.Request{
		CallID:  "call_4",
		Tool:    "bash",
		Action:  permission.ActionExecute,
		Command: strings.Repeat("rg --hidden --glob '!vendor' TODO ", 6),
	}
	for _, narrow := range []int{20, 40, width} {
		frame := dialog.Permission{Request: long}.Render(narrow)
		for _, line := range strings.Split(frame, "\n") {
			if got := ansi.StringWidth(line); got > narrow {
				t.Errorf("a line runs %d columns into a terminal %d wide: %q", got, narrow, line)
			}
		}
	}
}

func lastLine(t *testing.T, frame string) string {
	t.Helper()

	lines := strings.Split(ansi.Strip(frame), "\n")
	if len(lines) < 2 {
		t.Fatalf("the prompt drew %d line(s), so there is no line of answers under the question:\n%s",
			len(lines), frame)
	}
	return lines[len(lines)-1]
}
