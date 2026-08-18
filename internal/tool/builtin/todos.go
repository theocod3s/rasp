package builtin

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/theocod3s/rasp/internal/tool"
)

// TodoStatus is where one checklist item stands. The enum tag on
// [TodoItem.Status] lists these values a second time — reflection has no Go enum
// to read them from — so a status added here and not there is one the model is
// never told exists. A test holds the two together.
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
)

var todoStatuses = []TodoStatus{TodoPending, TodoInProgress, TodoCompleted}

// TodoItem is one line of the checklist. Position is identity: every call sends
// the whole list, so there is no id for the model and the UI to keep in step.
type TodoItem struct {
	Content string     `json:"content" desc:"What the step is, phrased as an instruction — add the parser tests."`
	Status  TodoStatus `json:"status" enum:"pending,in_progress,completed" desc:"pending for not started, in_progress for the one being worked on now, completed for finished."`
}

// TodosDetails is the checklist as the UI draws it.
type TodosDetails struct {
	Items []TodoItem
}

type todosInput struct {
	Items []TodoItem `json:"items" desc:"The complete checklist, in the order it should be shown. Send an empty array to clear it."`
}

const todosDescription = `Keep the checklist for the task in hand, so your plan is visible to the user and
correctable before you act on it. It stores a list and nothing else: it reads no
files, writes none, and runs no commands.

Send the complete list on every call — it replaces the stored one, so include the
items already completed. Call it with no arguments to read the list back, which is
how to recover the plan when it has scrolled out of your context.

Use it for work that takes several steps, or that the user handed you as a list,
and skip it when one step finishes the job. Mark an item in_progress when you
start it and completed the moment it is done, one call per change, rather than
filling in the statuses at the end.`

// NewTodos returns a todos tool holding its own list: a session gets one, a test
// gets one, and there is no package-level list for two of them to share. The list
// outlives a turn and dies with the process — durability is storage's job.
//
// Deliberately not [tool.Sequential] (design §3.2), because one sequential call
// makes the whole batch serial and a todos call beside six reads is the common
// case. A call swaps the entire list under the mutex, so concurrent calls cannot
// interleave into a list neither of them wrote; they can only race to be last,
// which two contradictory plans issued in one batch already deserve. An
// incremental operation — complete item 3 — would change that answer.
func NewTodos() tool.Tool {
	var (
		mu    sync.Mutex
		items []TodoItem
	)

	return tool.New("todos", todosDescription, func(_ context.Context, in todosInput) (tool.Result, error) {
		// An absent items key reads the list back instead of clearing it. Clearing
		// is an explicit empty array, and the asymmetry is deliberate: a model that
		// calls a tool with no arguments to see what it does should not lose the
		// plan it spent a turn writing.
		if in.Items == nil {
			mu.Lock()
			defer mu.Unlock()
			return todosResult(slices.Clone(items)), nil
		}

		for i, item := range in.Items {
			if problem := todoProblem(item); problem != "" {
				return tool.Result{
					IsError: true,
					Content: fmt.Sprintf("Item %d %s. Nothing was stored, so the list still reads as it did; send the whole list again with that fixed.", i+1, problem),
				}, nil
			}
		}

		mu.Lock()
		defer mu.Unlock()
		items = in.Items
		return todosResult(slices.Clone(items)), nil
	})
}

// todosResult wants a snapshot nothing else holds: the UI reads Details on its
// own goroutine, long after the call returned.
func todosResult(items []TodoItem) tool.Result {
	return tool.Result{
		Content: renderTodos(items),
		Title:   todosTitle(items),
		Details: &TodosDetails{Items: items},
	}
}

func renderTodos(items []TodoItem) string {
	// Never the empty string: llm.Block writes Content with omitempty, so an empty
	// list would arrive as a tool_result carrying no content at all — a tool that
	// did nothing, rather than a list with nothing in it.
	if len(items) == 0 {
		return "The todo list is empty."
	}

	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		// The status comes back as the token the model sends, so the next call is
		// a copy of this list with one word changed.
		fmt.Fprintf(&b, "%d. [%s] %s", i+1, item.Status, item.Content)
	}
	return b.String()
}

func todosTitle(items []TodoItem) string {
	if len(items) == 0 {
		return "no items"
	}
	done := 0
	for _, item := range items {
		if item.Status == TodoCompleted {
			done++
		}
	}
	return fmt.Sprintf("%d of %d done", done, len(items))
}

func todoProblem(item TodoItem) string {
	switch {
	case strings.TrimSpace(item.Content) == "":
		return "has no content, and a checklist line with nothing on it shows the user nothing"
	case !slices.Contains(todoStatuses, item.Status):
		return fmt.Sprintf("has status %q, which is not one of pending, in_progress or completed", item.Status)
	}
	return ""
}
