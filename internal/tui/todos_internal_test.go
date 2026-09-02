package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tool/builtin"
)

// todosCall runs a todos tool_start/tool_end pair through m and returns the
// model that leaves.
func todosCall(m tea.Model, callID string, items ...builtin.TodoItem) tea.Model {
	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventToolStart, CallID: callID, Tool: "todos"}})
	m, _ = m.Update(agentMsg{event: agent.Event{
		Kind: agent.EventToolEnd, CallID: callID, Tool: "todos",
		Result: &tool.Result{Details: &builtin.TodosDetails{Items: items}},
	}})
	return m
}

// TestSuccessiveTodosCallsUpdateOneCardInPlace is the ticket's central claim:
// the todos tool holds one list and rewrites it whole on every call
// (internal/tool/builtin/todos.go), so the card showing it is one card no
// matter how many different call ids the model used to reach it — not a new
// card appended beside the one the new call just made stale.
func TestSuccessiveTodosCallsUpdateOneCardInPlace(t *testing.T) {
	var m tea.Model = Model{}
	m = todosCall(m, "call_1", builtin.TodoItem{Content: "Write tests", Status: builtin.TodoPending})
	root := m.(Model)
	held := root.chat.Len()

	m = todosCall(m, "call_2",
		builtin.TodoItem{Content: "Write tests", Status: builtin.TodoCompleted},
		builtin.TodoItem{Content: "Ship it", Status: builtin.TodoPending},
	)

	root = m.(Model)
	if got := root.chat.Len(); got != held {
		t.Errorf("the conversation holds %d items after the second call and %d after the first — "+
			"a second call id added a card instead of updating the one already there", got, held)
	}

	frame := words(m.View().Content)
	if strings.Contains(frame, "☐ Write tests") {
		t.Errorf("the first call's stale line is still on screen:\n%s", frame)
	}
	if !strings.Contains(frame, "☑ Write tests") || !strings.Contains(frame, "☐ Ship it") {
		t.Errorf("the frame does not read the latest list:\n%s", frame)
	}
}

// TestAnEmptyTodosListLeavesNoCardOnScreen is "an empty list draws nothing
// rather than an empty frame" read at the model level: a call that reports no
// items leaves nothing a reader would see, not a card with a glyph and
// nothing under it.
func TestAnEmptyTodosListLeavesNoCardOnScreen(t *testing.T) {
	var m tea.Model = Model{}
	m = todosCall(m, "call_1")

	if frame := words(m.View().Content); strings.ContainsAny(frame, "☐☑▸") {
		t.Errorf("an empty list drew a card:\n%s", frame)
	}
}

// TestASecondTodosCallLeavesOtherFinishedCardsFrozen is the freeze guarantee
// the ticket names: updating the one card a todos call owns must not touch
// chat.View's cache for any other item. A card that redrew unprompted would
// read as though its call ran again, which is exactly what a frozen elapsed
// time proves it did not.
func TestASecondTodosCallLeavesOtherFinishedCardsFrozen(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	var m tea.Model = Model{now: func() time.Time { return now }}

	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"}})
	now = now.Add(2 * time.Second)
	m, _ = m.Update(agentMsg{event: agent.Event{
		Kind: agent.EventToolEnd, CallID: "call_1", Tool: "read",
		Result: &tool.Result{Title: "read auth.go (42 lines)"},
	}})

	m = todosCall(m, "todos_1", builtin.TodoItem{Content: "Write tests", Status: builtin.TodoPending})

	const finished = "read auth.go (42 lines) 2s"
	if frame := words(m.View().Content); !strings.Contains(frame, finished) {
		t.Fatalf("the frame does not read %q before the second todos call:\n%s", finished, frame)
	}

	now = now.Add(90 * time.Second)
	m = todosCall(m, "todos_2", builtin.TodoItem{Content: "Write tests", Status: builtin.TodoCompleted})

	frame := words(m.View().Content)
	if !strings.Contains(frame, finished) {
		t.Errorf("the read card's frozen elapsed moved when only the todos card changed:\n%s", frame)
	}
	if !strings.Contains(frame, "☑ Write tests") || strings.Contains(frame, "☐ Write tests") {
		t.Errorf("the todos card did not update to the latest list:\n%s", frame)
	}
}
