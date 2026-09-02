package chat_test

import (
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tool/builtin"
	"github.com/theocod3s/rasp/internal/tui/chat"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// TestATodosChecklistShowsStateAndOrderFromDetails is the acceptance
// criterion in one line: the glyph a card draws for each item, and the order
// they come in, is Details and nothing else — no re-sorting by status, no
// count of its own.
func TestATodosChecklistShowsStateAndOrderFromDetails(t *testing.T) {
	call := checklistCall(
		builtin.TodoItem{Content: "Write the parser test", Status: builtin.TodoCompleted},
		builtin.TodoItem{Content: "Fix the header parser", Status: builtin.TodoInProgress},
		builtin.TodoItem{Content: "Add a regression test", Status: builtin.TodoPending},
	)

	got := words(call.Render(wide))
	want := "☑ Write the parser test ▸ Fix the header parser ☐ Add a regression test"
	if got != want {
		t.Errorf("the checklist reads %q, want %q", got, want)
	}
}

// TestCompletedItemsAreMutedAndTheActiveItemIsMarked checks the styling
// itself, not just the glyph: words() strips colour, and a muted line reading
// the same words as a plain one would pass a test that only looked at those.
func TestCompletedItemsAreMutedAndTheActiveItemIsMarked(t *testing.T) {
	p := styles.For(styles.Dark)

	if got, want := checklistCall(builtin.TodoItem{Content: "Ship it", Status: builtin.TodoCompleted}).Render(wide),
		"  "+p.Muted.Render("☑ Ship it"); got != want {
		t.Errorf("a completed item reads %q, want %q", got, want)
	}
	if got, want := checklistCall(builtin.TodoItem{Content: "Fix the parser", Status: builtin.TodoInProgress}).Render(wide),
		"  "+p.CallRunning.Render("▸ Fix the parser"); got != want {
		t.Errorf("the in-progress item reads %q, want %q", got, want)
	}
	// Pending carries no style of its own — it is the line every other item is
	// set apart from.
	if got, want := checklistCall(builtin.TodoItem{Content: "Add a test", Status: builtin.TodoPending}).Render(wide),
		"  ☐ Add a test"; got != want {
		t.Errorf("a pending item reads %q, want %q", got, want)
	}
}

// TestAnEmptyChecklistDrawsNothing is the other half of "empty draws nothing
// rather than an empty frame": a todos call that succeeded and reported no
// items — read or explicitly cleared — leaves no card at all, not a headline
// with nothing under it.
func TestAnEmptyChecklistDrawsNothing(t *testing.T) {
	for _, items := range [][]builtin.TodoItem{nil, {}} {
		if got := checklistCall(items...).Render(wide); got != "" {
			t.Errorf("an empty list drew %q, want nothing", got)
		}
	}
}

// TestATodosCallWithoutTodosDetailsFallsBackToTheGenericCard. Details is an
// any: nothing stops a nil, or a payload some other tool wrote, from arriving
// on a call named "todos", and a checklist that assumed its shape would
// dereference a field that is not there.
func TestATodosCallWithoutTodosDetailsFallsBackToTheGenericCard(t *testing.T) {
	for _, tc := range []struct {
		name string
		call chat.Call
	}{
		{"no details at all", answered("todos", &tool.Result{})},
		{"the wrong details type", answered("todos", &tool.Result{Details: &tool.DiffDetails{Path: "auth.go"}})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := words(tc.call.Render(wide)), "✓ todos"; got != want {
				t.Errorf("the card reads %q, want %q", got, want)
			}
		})
	}
}

// TestAFailedTodosCallShowsTheRefusalNotAChecklist. A rejected item never
// reaches Details (todosResult is only built on the success path), so a
// reader has to see why through the same refusal line every other failing
// tool draws.
func TestAFailedTodosCallShowsTheRefusalNotAChecklist(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  *tool.Result
	}{
		{"no details, as the real tool sends it", &tool.Result{
			IsError: true,
			Content: "Item 1 has no content, and a checklist line with nothing on it shows the user nothing.",
		}},
		// The real tool never pairs IsError with Details (todosResult is only
		// built on the success path), but nothing in the Result type stops it —
		// and IsError must win regardless of what Details holds.
		{"details present anyway", &tool.Result{
			IsError: true,
			Content: "refused",
			Details: &builtin.TodosDetails{Items: []builtin.TodoItem{{Content: "x", Status: builtin.TodoPending}}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := words(answered("todos", tc.res).Render(wide))
			if !strings.Contains(got, "refused") && !strings.Contains(got, "has no content") {
				t.Errorf("the card drops the refusal:\n%s", got)
			}
			if strings.ContainsAny(got, "☐☑▸") {
				t.Errorf("a failing call drew checklist glyphs:\n%s", got)
			}
		})
	}
}

// TestATodosCallStillRunningIsTheGenericSpinner. There is no Details until
// the call answers, so a card still waiting on one draws the same spinner
// every other tool gets rather than an empty checklist.
func TestATodosCallStillRunningIsTheGenericSpinner(t *testing.T) {
	call := chat.Call{Name: "todos", State: chat.CallRunning}
	if got, want := words(call.Render(wide)), "⠋ todos"; got != want {
		t.Errorf("a running todos call reads %q, want %q", got, want)
	}
}

func checklistCall(items ...builtin.TodoItem) chat.Call {
	return answered("todos", &tool.Result{Details: &builtin.TodosDetails{Items: items}})
}
