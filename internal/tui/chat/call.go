package chat

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

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

// The status glyphs a card's line leads with — done, failed, and the rest a
// call waiting to start, before either is known.
const (
	glyphQueued = "○"
	glyphDone   = "✓"
	glyphFailed = "✗"
)

// beatInterval is model.go's tickInterval, duplicated rather than imported:
// tui imports chat to draw cards, so the dependency cannot run the other way.
// Elapsed is stamped off that same clock (model.go's beat), so reading it
// here rather than keeping a second timer is what leaves the running glyph
// unable to disagree with the elapsed time drawn beside it.
const beatInterval = 100 * time.Millisecond

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
	marker := c.marker(c.opens())
	head := inset(wrap(c.headline(), width-len(cardIndent)), cardIndent)
	head = marker + strings.TrimPrefix(head, cardIndent)
	head = c.paint(head, marker)

	if !c.Expanded {
		return head
	}
	// Only now, and never wrapped afterwards: wrapping a diff line draws one
	// changed line as two, and a collapsed card that built its diff anyway would
	// rebuild it on every frame after a collapse, which is exactly when the
	// freeze that spared it has been dropped.
	body := c.body(width)
	if body == "" {
		return head
	}
	return head + "\n" + inset(body, cardIndent)
}

// inner is the width left inside a two-column indent, never zero or less. The
// diff renderer needs that floor strictly — it cuts a line to width rather
// than wrapping it, and zero reads as "no size reported yet, do not cut", so a
// real terminal too narrow for the indent must not arrive spelled that way or
// every diff line goes out at full length. Every caller here shares the same
// floor rather than each working out its own version of it, even where wrap
// would have read zero just as safely on its own.
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

// headline is the card's one line, plain text throughout: a status glyph, the
// tool's name, the argument summary the tool wrote, and — once a call has
// taken long enough for anyone to notice — how long for. Coloured afterwards,
// by paint, rather than here — see paint for why.
func (c Call) headline() string {
	line := c.glyphChar() + " " + c.Name
	if s := c.summary(); s != "" {
		line += " " + s
	}
	if d := elapsed(c.Elapsed); d != "" {
		line += " " + d
	}
	return line
}

// glyphState is which of the four the status glyph draws as, decided once so
// glyphChar and glyphStyle read the same answer rather than each working it
// out — a queued call painted the done colour is not a state glyphChar or
// glyphStyle could reach on their own, only the two disagreeing about it.
type glyphState int

const (
	glyphStateQueued glyphState = iota
	glyphStateRunning
	glyphStateDone
	glyphStateFailed
)

func (c Call) glyphState() glyphState {
	switch c.State {
	case CallQueued:
		return glyphStateQueued
	case CallRunning:
		return glyphStateRunning
	}
	if c.Result != nil && c.Result.IsError {
		return glyphStateFailed
	}
	return glyphStateDone
}

// glyphChar is the plain-text status mark headline builds the line from — a
// circle for a call with nothing to report yet, the running spinner's current
// frame, or a check or a cross once the result is in.
func (c Call) glyphChar() string {
	switch c.glyphState() {
	case glyphStateQueued:
		return glyphQueued
	case glyphStateRunning:
		return styles.Spinner[spinnerIndex(c.Elapsed)]
	case glyphStateFailed:
		return glyphFailed
	}
	return glyphDone
}

// glyphStyle is glyphChar's colour, kept apart from it because it is wanted
// only once headline's plain text has already been measured and wrapped
// (paint).
func (c Call) glyphStyle(p styles.Palette) lipgloss.Style {
	switch c.glyphState() {
	case glyphStateQueued:
		return p.Muted
	case glyphStateRunning:
		return p.CallRunning
	case glyphStateFailed:
		return p.CallFailed
	}
	return p.CallDone
}

// spinnerIndex is the running glyph's frame for a call this long, one
// revolution a second. Read off Elapsed rather than counted per tick, so a
// beat that arrived late or twice never puts the animation somewhere else
// than the clock says it is.
func spinnerIndex(d time.Duration) int {
	if d < 0 {
		return 0
	}
	return int(d/beatInterval) % len(styles.Spinner)
}

// summary is the argument summary a finished call's own tool wrote, minus the
// tool's name where the Title repeats it — read, ls, grep and find each write
// one that way, while edit and write lead with the path and bash with the
// command, so trimming a name that never opens the Title is a no-op rather
// than a wrong cut.
//
// A queued or running call has no result yet, and rasp has no other route to
// a call's arguments: Details is the tool's report on what it did, not a
// preview of what it was asked to do. A failing built-in writes no Title at
// all, and its Content is the sentence explaining the refusal.
func (c Call) summary() string {
	if c.State != CallDone || c.Result == nil {
		return ""
	}

	text := firstLine(c.Result.Title)
	if c.Result.IsError {
		if text == "" {
			text = firstLine(c.Result.Content)
		}
		return text
	}
	return strings.TrimPrefix(text, c.Name+" ") + c.fuzzy()
}

// paint colours headline's glyph and name after wrap has already measured and
// broken the plain line it built. Colouring before would hand wrap an escape
// sequence to count: wrap measures runes, every byte of one is a rune, and a
// styled prefix would be read as that many characters of visible text and cut
// the line short of where it actually ends. Only the first line is touched,
// since the glyph and name live there whether or not a long summary wrapped
// on beneath them.
//
// marker is the same string Render already put at the front of head, passed
// in rather than re-derived, so its width is read off the exact bytes drawn
// there instead of assumed.
func (c Call) paint(head, marker string) string {
	first, rest, hasRest := strings.Cut(head, "\n")

	markerRunes := utf8.RuneCountInString(marker)
	runes := []rune(first)
	name := []rune(c.Name)
	nameEnd := markerRunes + 2 + len(name) // marker, glyph, the space, the name
	if len(runes) < nameEnd {
		// Too narrow for the glyph and name to have survived the wrap uncut,
		// so there is nothing here safe to slice and colour.
		return head
	}

	p := styles.For(c.Background)
	glyph := c.glyphStyle(p).Render(string(runes[markerRunes : markerRunes+1]))
	styledName := p.CallName.Render(string(runes[markerRunes+2 : nameEnd]))
	first = string(runes[:markerRunes]) + glyph + " " + styledName + string(runes[nameEnd:])

	if !hasRest {
		return first
	}
	return first + "\n" + rest
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
// file change produced, or the output the model was given. The guard repeats
// the cheap half of opens rather than calling it, so empty here and false there
// stay the same set without a second pass over the diff.
func (c Call) body(width int) string {
	if c.State != CallDone || c.Result == nil {
		return ""
	}
	// Drawn and then tested, rather than asked and then drawn: HasDiff walks the
	// whole diff to answer, and this is the one caller that has to walk it
	// anyway. Empty from a diff and no diff at all are the same card.
	text := c.text()
	if !c.Result.IsError {
		if drawn := diffview.Render(c.unified(), inner(width), styles.For(c.Background)); drawn != "" {
			return drawn
		}
		if c.Name == "read" {
			if start, lines, ok := readLines(text); ok {
				return diffview.Numbered(lines, start, inner(width), styles.For(c.Background))
			}
		}
	}
	return wrap(text, width-len(cardIndent))
}

// readLines undoes the read tool's own line-number prefix — "the line number
// and a tab", by its own description, added by the tool and not part of the
// file — recovering both the lines and the offset the window opened at.
// ReadDetails does carry that offset as a field, but it lives in tool/builtin,
// a leaf package outside this UI's reach; the prefix already in Content is
// the route open to this card, and it is required to survive untouched into
// any edit or write the model makes from what it read, so parsing it back out
// is reading a contract the tool already keeps rather than a coincidence.
//
// A read that failed, or one whose file had nothing in it, put a plain
// sentence in Content instead — no line carries a number, so the first cut
// finds no tab and ok is false, and the card falls back to that sentence
// verbatim rather than guessing at a window.
func readLines(content string) (start int, lines []string, ok bool) {
	split := strings.Split(content, "\n")
	lines = make([]string, 0, len(split))
	for i, line := range split {
		numStr, rest, found := strings.Cut(line, "\t")
		if !found {
			return 0, nil, false
		}
		n, err := strconv.Atoi(strings.TrimSpace(numStr))
		if err != nil {
			return 0, nil, false
		}
		if i == 0 {
			start = n
		}
		lines = append(lines, rest)
	}
	return start, lines, true
}

// text is the tool's output as the card shows it, and nothing when the line
// above already says the whole of it: a failing tool writes no title, so that
// line carries its content's first line, and where that is all there is,
// opening the card would say the same sentence twice.
func (c Call) text() string {
	if c.Result == nil {
		return ""
	}
	// Newlines only: leading spaces on the first line are output, not padding,
	// and a column of right-aligned counts loses its top row to a trim.
	content := strings.Trim(c.Result.Content, "\n")

	// Whether there is anything to show is the other question, and that one does
	// ask about whitespace: wrap takes trailing spaces away to nothing, so output
	// kept here and lost there hangs a marker over a card that opens onto a
	// blank line.
	if strings.TrimSpace(content) == "" {
		return ""
	}
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
	if r := d.Round(beatInterval); r > 0 {
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
