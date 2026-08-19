package chat

import (
	"strings"
	"sync"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
)

// md is the conversation's markdown renderer. One value for the whole package:
// the memo inside it is worth nothing split between callers — only one message
// is ever still arriving.
var md = &markdown{draw: glamourBlock}

type markdown struct {
	mu sync.Mutex

	// draw renders one whole markdown document to terminal text. A field rather
	// than a plain call so a test can see exactly what reached glamour, which is
	// the entire claim the split below makes.
	draw func(src string, width int) string

	// done is the head of the message already proved final, and its rendering.
	// One entry, because one message arrives at a time; keyed by the bytes it was
	// taken from, so a hit is the same markdown at the same width whichever
	// message asks for it.
	done cache
}

type cache struct {
	width int
	src   string
	out   string
}

// render draws markdown that may still be arriving, re-rendering only what has
// changed since the last frame. Everything up to the boundary is rendered once
// and kept; each frame pays for the tail alone, and for whatever the boundary
// has swept past since (internals §4.4).
//
// Rendered in pieces rather than whole because two renders joined are not
// generally the same as one — glamour resets its wrap state between calls — so
// the pieces are only ever cut where stableBoundary proves the seam invisible.
func (m *markdown) render(src string, width int) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	n := stableBoundary(src)
	switch {
	case m.done.width != width || n < len(m.done.src) || !strings.HasPrefix(src, m.done.src):
		m.done = cache{width: width, src: src[:n], out: m.block(src[:n], width)}
	case n > len(m.done.src):
		m.done.out = join(m.done.out, m.block(src[len(m.done.src):n], width))
		m.done.src = src[:n]
	}
	return join(m.done.out, m.block(src[n:], width))
}

func (m *markdown) block(src string, width int) string {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	return m.draw(src, width)
}

// join puts two rendered pieces back together with the one blank line glamour
// leaves between blocks of a single document.
func join(head, tail string) string {
	switch {
	case head == "":
		return tail
	case tail == "":
		return head
	}
	return head + "\n\n" + tail
}

// renderersMu guards renderers and every call into one. Building a
// TermRenderer costs ~27µs, so this memoizes one per width instead of paying
// that on every block — but a memoized renderer is now shared between calls,
// and glamour is not reentrant: two callers at the same width must not run
// Render at once.
var (
	renderersMu sync.Mutex
	renderers   = map[int]*glamour.TermRenderer{}
)

// renderer returns the TermRenderer memoized for width, building one on the
// first call at that width. Callers hold renderersMu.
func renderer(width int) (*glamour.TermRenderer, error) {
	if r, ok := renderers[width]; ok {
		return r, nil
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(styles.DarkStyle),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil, err
	}
	renderers[width] = r
	return r, nil
}

func glamourBlock(src string, width int) string {
	// Glamour reads a wrap width of zero as "do not wrap", which is what a
	// terminal whose size has not arrived yet wants drawn.
	if width < 0 {
		width = 0
	}

	renderersMu.Lock()
	defer renderersMu.Unlock()

	r, err := renderer(width)
	if err == nil {
		var out string
		if out, err = r.Render(src); err == nil {
			return trimBlankLines(out)
		}
	}
	// The text still has to appear. A message the renderer choked on is shown as
	// what the model sent, which is worse-looking and never missing.
	return wrap(src, width)
}

// trimBlankLines drops the leading and trailing lines that show nothing.
// Glamour frames a document in blank lines, and two renderings joined by one
// blank line have to read as the one document they were cut out of.
func trimBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	first, last := 0, len(lines)
	for first < last && blankLine(lines[first]) {
		first++
	}
	for last > first && blankLine(lines[last-1]) {
		last--
	}
	return strings.Join(lines[first:last], "\n")
}

// blankLine reports a line that puts no glyph on the screen. Glamour pads its
// output to the wrap width, so a line that is visually empty is a run of styled
// spaces rather than an empty string.
func blankLine(line string) bool {
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == escape:
			for i < len(line) && line[i] != 'm' {
				i++
			}
		case line[i] == ' ' || line[i] == '\t':
		default:
			return false
		}
	}
	return true
}

const escape = 0x1b
