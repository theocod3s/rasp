package chat

import (
	"strings"
	"time"

	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tui/diffview"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// A card's first two columns: the marker saying there is more under it, or the
// margin glamour sets a reply in by, so the two line up.
const (
	cardIndent    = "  "
	cardCollapsed = "▸ "
	cardExpanded  = "▾ "
)

// CallState is how far a tool call has got.
type CallState int

const (
	// CallQueued is a call the model asked for and nothing has started. A batch
	// is announced whole and then dispatched, so this is the rest of it while
	// the first runs, and a call waiting on the user's approval for as long as
	// the question stands.
	CallQueued CallState = iota
	CallRunning
	CallDone
)

// Call is one tool call, drawn as a card: one line saying what ran and how it
// went, and the tool's own output under it once expanded.
//
// It holds the whole [tool.Result] rather than a copy of the parts it draws.
// Details is the tool's payload for the UI and never reaches the model, so
// reading it here is the only route those facts have to a reader — and reading
// it here rather than having the tool hand back a drawn card is what leaves the
// tools usable by a frontend that draws nothing (design §3.4).
type Call struct {
	Name   string
	State  CallState
	Result *tool.Result // nil until the call is answered

	// Elapsed is how long the call has run, as of the frame that asked. Passed
	// in rather than measured here, so Render is a function of the item alone:
	// the view freezes a finished item's text and hands that string back for
	// every frame after (internals §4.5), which a card timing itself would make
	// quietly untrue.
	Elapsed time.Duration

	Expanded bool

	// Background is the terminal's, and picks the palette a diff is drawn in.
	// Passed in for the same reason Elapsed is.
	Background styles.Background
}

func (c Call) Finished() bool { return c.State == CallDone }

// HasDiff reports that the card has a file change to show rather than a tool's
// text output. Such a card is opened for the reader rather than by them: a
// change one keypress short of visible is a path and a line count, which is the
// gap this UI exists to close. A failing call is never one, however much it
// changed — its content says what went wrong, and that comes first.
func (c Call) HasDiff() bool {
	return c.Result != nil && !c.Result.IsError && diffview.Draws(c.unified())
}

func (c Call) Render(width int) string {
	// The headline is set in like the body and then gives its first two columns
	// back to the marker. Wrapped whole instead, a summary too long for the
	// terminal continues at column zero and reads as a line of its own.
	head := inset(wrap(c.headline(), inner(width)), cardIndent)
	head = c.marker(c.opens()) + strings.TrimPrefix(head, cardIndent)

	if !c.Expanded {
		return head
	}
	// Only now, and to its own width rather than wrapped afterwards: wrapping a
	// diff line draws one changed line as two, and a collapsed card that built
	// its diff anyway would rebuild it on every frame after a collapse, which is
	// exactly when the freeze that spared it has been dropped.
	body := c.body(inner(width))
	if body == "" {
		return head
	}
	return head + "\n" + inset(body, cardIndent)
}

// inner is the width left inside the card's indent, and never zero or less —
// which everything downstream reads as "no size reported yet, do not cut". A
// real terminal too narrow for the indent must not arrive spelled that way, or
// its diff lines go out at full length and wrap.
func inner(width int) int {
	if width <= 0 {
		return width
	}
	return max(1, width-len(cardIndent))
}

func (c Call) marker(expandable bool) string {
	switch {
	case !expandable:
		return cardIndent
	case c.Expanded:
		return cardExpanded
	}
	return cardCollapsed
}

// headline is the card's one line: what ran, how it went, and — once it has
// taken long enough for anyone to notice — how long for.
func (c Call) headline() string {
	line := c.Name + " " + c.outcome()
	if d := elapsed(c.Elapsed); d != "" {
		line += " " + d
	}
	return line
}

// outcome is the part of the line the tool wrote. A Title already opening with
// the tool's own name is used whole — read, ls, grep and find each write one
// that way, while edit and write lead with the path and bash with the command —
// so the line names the tool exactly once whichever kind arrives.
//
// A failing built-in writes no Title at all, and its Content is the sentence
// explaining the refusal, which is what the line falls back to.
func (c Call) outcome() string {
	switch {
	case c.State == CallQueued:
		return "queued"
	case c.State == CallRunning:
		return "running"
	case c.Result == nil:
		return "done"
	}

	summary := firstLine(c.Result.Title)
	if c.Result.IsError {
		if summary == "" {
			summary = firstLine(c.Result.Content)
		}
		if summary == "" {
			return "failed"
		}
		return "failed: " + summary
	}
	if summary == "" {
		summary = "done"
	}
	return strings.TrimPrefix(summary, c.Name+" ") + c.fuzzy()
}

// fuzzy marks an edit whose old_string was found by a whitespace-normalized
// rung of the match ladder rather than byte for byte. On the collapsed line
// rather than in the body, because the file no longer reads as the model wrote
// it and a reader who has to open the card to learn that learns it too late.
func (c Call) fuzzy() string {
	if d := c.diff(); d != nil && d.Fuzzy {
		return " (whitespace-normalized match)"
	}
	return ""
}

// opens reports that the card has something under its line — the one place
// that rule lives, so the marker and the body cannot disagree about whether
// there is anything to open onto.
func (c Call) opens() bool {
	if c.State != CallDone || c.Result == nil {
		return false
	}
	return c.HasDiff() || c.text() != ""
}

// body is what expanding the card shows, already drawn to width: the diff a
// file change produced, or the output the model was given.
// The guard is the cheap half of opens rather than a call to it, which would
// split the diff a second time. Empty here and false there stay the same set.
func (c Call) body(width int) string {
	if c.State != CallDone || c.Result == nil {
		return ""
	}
	if c.HasDiff() {
		return diffview.Render(c.unified(), width, styles.For(c.Background))
	}
	return wrap(c.text(), width)
}

// text is the tool's output as the card shows it, and nothing when the line
// above already says the whole of it: a failing tool writes no title, so that
// line carries its content's first line, and where that is all there is,
// opening the card would say the same sentence twice.
func (c Call) text() string {
	content := strings.Trim(c.Result.Content, "\n")
	if c.Result.IsError && c.Result.Title == "" && content == firstLine(content) {
		return ""
	}
	return content
}

// unified is the diff a tool that changed a file put in Details, and empty for
// everything else: an edit that turned out to change nothing, which go-udiff
// renders as no text at all, and an MCP server's structured output, which is
// arbitrary decoded JSON a card guessing at its shape would invent facts from.
func (c Call) unified() string {
	if d := c.diff(); d != nil {
		return d.Unified
	}
	return ""
}

// diff is the payload a tool that changed a file leaves for the UI, and nil for
// everything else — an MCP server's arbitrary decoded JSON included. The one
// route to that field, because a tool with nothing to report can leave a typed
// nil in an any, and an assertion that asked only ok would dereference it.
func (c Call) diff() *tool.DiffDetails {
	if c.Result == nil {
		return nil
	}
	d, _ := c.Result.Details.(*tool.DiffDetails)
	return d
}

// elapsed is a duration as a card says it, and nothing for one too short to be
// worth the reader's attention — which is most tool calls.
func elapsed(d time.Duration) string {
	if r := d.Round(100 * time.Millisecond); r > 0 {
		return r.String()
	}
	return ""
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

// inset sets text in by prefix, leaving blank lines blank rather than filling
// them with the trailing spaces a terminal would keep.
func inset(text, prefix string) string {
	var b strings.Builder
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		if line != "" {
			b.WriteString(prefix)
			b.WriteString(line)
		}
	}
	return b.String()
}
