package chat

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// Caret marks a line the user wrote — their prompts here and the line they are
// typing below, so a conversation of two speakers reads as one.
const Caret = "› "

// userBar is the accent every line of a prompt opens with, run down the left
// edge — Claude Code's `>` box, drawn as a rule instead of a rectangle. Its
// width matches cardIndent's: two columns, the same margin a tool card's own
// marker column takes, so the transcript keeps one gutter width whichever kind
// of item is standing in it.
const userBar = "▌"

// Message is one turn of the conversation, a prompt or a reply, as the view
// draws it.
type Message struct {
	Content llm.Message

	// Done is set once the message has stopped arriving. Every delta carries the
	// whole accumulation (design §3.1), so a reply still streaming is this same
	// item rebuilt from the latest one with Done false.
	Done bool

	// Background is the terminal's, and picks the palette thinking is drawn in.
	// Fixed when the item is made: nothing repaints a reply the way it repaints
	// a card, because the model keeps no copy of one it has committed (model.go).
	Background styles.Background
}

func (m Message) Finished() bool { return m.Done }

// Render draws a reply as markdown and a prompt as the characters the user
// typed: a prompt is not a document, and running one through a markdown
// renderer would eat the punctuation they meant literally.
//
// A reply that thought first draws the thinking above it, faint and whole, and
// deliberately not through the markdown renderer: keeping it out leaves the
// stable-prefix proof (boundary.go) about the reply alone.
func (m Message) Render(width int) string {
	if m.Content.Role == llm.RoleUser {
		text := spoken(m.Content)
		if text == "" {
			return ""
		}
		return userBlock(text, width, styles.For(m.Background).UserBar)
	}

	var head, body string
	// Trimmed: an accumulation caught mid-stream often ends on the newline before
	// the next paragraph, and a faint blank line under the segment reads as a gap.
	// Indented two columns, the same margin a card's own body is set in by, so
	// the segment a reader is meant to skim past reads as subordinate the same
	// way everywhere else in the transcript says it.
	if text := strings.TrimSpace(thinking(m.Content)); text != "" {
		head = inset(paint(text, styles.For(m.Background).Faint, width-len(cardIndent)), cardIndent)
	}
	// Guarded rather than left to the renderer: an empty string shares no prefix
	// with the memo, so rendering one drops the head of the arriving reply
	// (markdown.go).
	if text := spoken(m.Content); text != "" {
		body = md.render(text, width)
	}
	return join(head, body)
}

// spoken is what the model said and thinking is what it worked through first.
// Two strings rather than one because only one of them is markdown; a tool call
// is an item of its own either way.
func spoken(msg llm.Message) string   { return content(msg, llm.BlockText) }
func thinking(msg llm.Message) string { return content(msg, llm.BlockThinking) }

func content(msg llm.Message, kind llm.BlockType) string {
	var b strings.Builder
	for _, block := range msg.Content {
		if block.Type == kind {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

// userBlock draws a prompt as its own block rather than a line indistinguishable
// from whatever the model said above it: the bar opens the first line, the same
// way a card's own marker opens its headline and nothing else, so a prompt long
// enough to wrap reads as one block set in by a margin rather than a bar
// repeated down its own left edge.
func userBlock(text string, width int, bar lipgloss.Style) string {
	lines := strings.Split(wrap(Caret+text, width-len(cardIndent)), "\n")
	for i, line := range lines {
		if i == 0 {
			lines[i] = bar.Render(userBar) + " " + line
			continue
		}
		lines[i] = cardIndent + line
	}
	return strings.Join(lines, "\n")
}

// paint wraps text to width and styles it a line at a time: Lip Gloss renders a
// multi-line string as a block, padding every line out to the longest with
// spaces the reader would find again on the end of anything they copied.
func paint(text string, style lipgloss.Style, width int) string {
	lines := strings.Split(wrap(text, width), "\n")
	for i, line := range lines {
		lines[i] = style.Render(line)
	}
	return strings.Join(lines, "\n")
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
