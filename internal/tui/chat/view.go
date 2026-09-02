package chat

import "strings"

// Item is one entry in the conversation: a message, a tool call, anything the
// view draws as its own block of lines.
type Item interface {
	// Render draws the item wrapped to width, with no trailing newline. A width
	// of zero or less is a terminal whose size has not arrived yet, and nothing
	// wraps.
	Render(width int) string

	// Finished reports that the item cannot change again. The view renders such
	// an item once per width and hands that string back for every frame after,
	// so an item that answers true and then changes is drawn as it was.
	Finished() bool
}

// View is the conversation: its items in the order they arrived, each holding
// the last string it rendered to. The zero View is empty and ready.
//
// A value rather than a pointer, because the Bubble Tea model is one: Update
// takes a copy and returns it. Copies share their items, which is what carries
// a warm cache across an Update — and why only the model Update returned goes
// on: two copies that each grow the conversation disagree about where an item
// is.
type View struct {
	items []cell

	// at maps the key an item was added under to its position, so Set replaces
	// in place instead of moving the item to the end of the conversation.
	at map[string]int
}

type cell struct {
	item Item

	// frozen says text is this item's final form at width, and is set only once
	// the item is finished. Anything still arriving is drawn again every frame,
	// which is the cost the freeze bounds to the one item still moving
	// (internals §4.5).
	frozen bool
	width  int
	text   string
}

// Append adds an item to the end of the conversation. Nothing can name it
// again, which is what an item already in its final form wants.
func (v *View) Append(item Item) {
	v.items = append(v.items, cell{item: item})
}

// Set adds an item under key, or replaces the one already there without moving
// it — how something that changes after it is first drawn is updated: a reply
// still streaming, a tool call that finishes. Replacing drops the cached render
// along with the item it was taken from.
func (v *View) Set(key string, item Item) {
	if i, ok := v.at[key]; ok {
		v.items[i] = cell{item: item}
		return
	}
	if v.at == nil {
		v.at = make(map[string]int)
	}
	v.at[key] = len(v.items)
	v.items = append(v.items, cell{item: item})
}

// Len is how many items the conversation holds.
func (v *View) Len() int { return len(v.items) }

// Render draws the whole conversation, a blank line between each pair of items
// so the transcript reads as separate turns rather than one dense scroll. An
// item that draws nothing takes no line of its own and opens no gap either — a
// step whose reply was only a tool call has no text to show, and the item
// before it is followed by exactly one blank line, not two.
func (v *View) Render(width int) string {
	var b strings.Builder
	open := false
	for i := range v.items {
		text := v.items[i].render(width)
		if text == "" {
			continue
		}
		if open {
			b.WriteByte('\n')
		}
		b.WriteString(text)
		b.WriteByte('\n')
		open = true
	}
	return b.String()
}

func (c *cell) render(width int) string {
	if c.frozen && c.width == width {
		return c.text
	}
	text := c.item.Render(width)
	if c.item.Finished() {
		c.frozen, c.width, c.text = true, width, text
	}
	return text
}
