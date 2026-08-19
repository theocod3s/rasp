package chat

import (
	"strings"

	"github.com/theocod3s/rasp/internal/llm"
)

// Caret marks a line the user wrote — their prompts here and the line they are
// typing below, so a conversation of two speakers reads as one.
const Caret = "› "

// Message is one turn of the conversation, a prompt or a reply, as the view
// draws it.
type Message struct {
	Content llm.Message

	// Done is set once the message has stopped arriving. Every delta carries the
	// whole accumulation (design §3.1), so a reply still streaming is this same
	// item rebuilt from the latest one with Done false.
	Done bool
}

func (m Message) Finished() bool { return m.Done }

// Render draws a reply as markdown and a prompt as the characters the user
// typed: a prompt is not a document, and running one through a markdown
// renderer would eat the punctuation they meant literally.
func (m Message) Render(width int) string {
	text := spoken(m.Content)
	if text == "" {
		return ""
	}
	if m.Content.Role == llm.RoleUser {
		return wrap(Caret+text, width)
	}
	return md.render(text, width)
}

// spoken is the part of a message a reader is meant to see. Thinking is left
// out, and a tool call is an item of its own.
func spoken(msg llm.Message) string {
	var b strings.Builder
	for _, block := range msg.Content {
		if block.Type == llm.BlockText {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

// Call is one tool call: its name, and how far it has got.
type Call struct {
	Name   string
	Done   bool
	Failed bool
}

func (c Call) Finished() bool { return c.Done }

func (c Call) Render(width int) string {
	state := "running"
	switch {
	case c.Failed:
		state = "failed"
	case c.Done:
		state = "done"
	}
	return wrap(c.Name+": "+state, width)
}

// wrap breaks text so no line runs past width, at the last space that fits and
// inside a word only when no space does. A width of zero or less is a size
// nobody has reported yet, and nothing is broken.
//
// Measured in runes rather than terminal cells, so a line of double-width
// characters still overruns. Measuring cells means measuring styled text, which
// belongs with the renderer that styles it.
func wrap(text string, width int) string {
	if width <= 0 || text == "" {
		return text
	}
	var b strings.Builder
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(wrapLine(line, width))
	}
	return b.String()
}

func wrapLine(line string, width int) string {
	runes := []rune(strings.TrimRight(line, " "))
	if len(runes) <= width {
		return string(runes)
	}

	var b strings.Builder
	for len(runes) > width {
		cut := width
		for i := width; i > 0; i-- {
			if runes[i] == ' ' {
				cut = i
				break
			}
		}
		b.WriteString(strings.TrimRight(string(runes[:cut]), " "))
		b.WriteByte('\n')
		for cut < len(runes) && runes[cut] == ' ' {
			cut++
		}
		runes = runes[cut:]
	}
	b.WriteString(string(runes))
	return b.String()
}
