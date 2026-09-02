package chat

import (
	"strings"

	"github.com/theocod3s/rasp/internal/tool/builtin"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// in_progress gets its own marker rather than reusing pending's box, so the
// one item a reader most wants to find does not rely on colour alone to say so.
const (
	checklistPending = "☐ "
	checklistDone    = "☑ "
	checklistActive  = "▸ "
)

// todosItems is the checklist a finished, successful todos call reports, in
// the order Details already holds it — the UI adds no ordering of its own.
// False for anything else: still running, failed, or a different tool.
func (c Call) todosItems() ([]builtin.TodoItem, bool) {
	if c.Name != "todos" || c.State != CallDone || c.Result == nil || c.Result.IsError {
		return nil, false
	}
	d, ok := c.Result.Details.(*builtin.TodosDetails)
	if !ok {
		return nil, false
	}
	return d.Items, true
}

// checklist draws a todos call as its list rather than as a headline and a
// count. Nothing for an empty one, rather than a card with nothing under it —
// the tool already uses an empty array to mean the plan was cleared
// (todosInput.Items).
func checklist(items []builtin.TodoItem, width int, bg styles.Background) string {
	if len(items) == 0 {
		return ""
	}
	p := styles.For(bg)
	w := inner(width)
	lines := make([]string, len(items))
	for i, item := range items {
		lines[i] = checklistLine(item, w, p)
	}
	return inset(strings.Join(lines, "\n"), cardIndent)
}

func checklistLine(item builtin.TodoItem, width int, p styles.Palette) string {
	switch item.Status {
	case builtin.TodoCompleted:
		return paint(checklistDone+item.Content, p.Muted, width)
	case builtin.TodoInProgress:
		return paint(checklistActive+item.Content, p.CallRunning, width)
	default:
		return wrap(checklistPending+item.Content, width)
	}
}
