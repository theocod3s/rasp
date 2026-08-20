package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

const (
	statusSep = " · "

	// costSegment is the one thing here nothing can fill in: a price is per model
	// and comes from the catalog (design §10.2), which nothing fetches yet. A
	// table written here instead would put a wrong number where a reader looks
	// for the bill, and that is worse than an absent one.
	costSegment = "cost —"
)

// status is the line between the conversation and the input. View draws it from
// the model's own fields rather than as an item in the conversation, which is
// what leaves a frame that moved only these numbers costing no item render at
// all (internals §4.5).
type status struct {
	model string
	mode  permission.Mode

	// context is what the last model call accounted for, prompt and reply
	// together — a floor for the next request rather than the whole of it, since
	// the tool results and prompts added since are estimated off the transcript
	// (design §11), which the UI does not hold. Not a share of the window: that
	// size comes from the catalog, along with the price.
	context int

	// spent is what the finished turns cost, running the turn in flight. Two
	// fields because a turn's end brings the loop's own total for it
	// (agent.Event.Usage), summed over the same messages the running total was
	// built from: it replaces that sum, and adding would count every turn twice.
	spent, running llm.Usage
}

func (s status) call(u llm.Usage) status {
	s.context = sent(u) + u.Output
	s.running = addUsage(s.running, u)
	return s
}

func (s status) turnEnded(u llm.Usage) status {
	s.spent = addUsage(s.spent, u)
	s.running = llm.Usage{}
	return s
}

// Render draws the line at width, dropping whole segments from the right until
// it fits — elided, half a token count reads as a smaller one. The mode comes
// first and survives every drop.
func (s status) Render(width int, bg styles.Background) string {
	palette := styles.For(bg)
	total := addUsage(s.spent, s.running)

	segments := []string{string(s.modeName())}
	if s.model != "" {
		segments = append(segments, s.model)
	}
	segments = append(segments,
		"ctx "+tokens(s.context),
		"in "+tokens(sent(total)),
		"out "+tokens(total.Output),
		costSegment,
	)
	for len(segments) > 1 && width > 0 && lineWidth(segments) > width {
		segments = segments[:len(segments)-1]
	}

	mode := s.tint(segments[0], palette)
	if len(segments) == 1 {
		// The one place anything is cut. A terminal too narrow even for the mode
		// says as much of it as fits, rather than dropping the line entirely on
		// the screens with least room to work out the mode from anything else.
		if width > 0 && ansi.StringWidth(mode) > width {
			return ansi.Truncate(mode, width, "")
		}
		return mode
	}
	return mode + palette.Muted.Render(statusSep+strings.Join(segments[1:], statusSep))
}

// modeName draws the default for a caller that named no mode, rather than a
// gap: a status line able to leave the mode blank fails the one thing it is on
// the screen for.
func (s status) modeName() permission.Mode {
	if s.mode == "" {
		return permission.ModeManual
	}
	return s.mode
}

// tint colours the mode (design §7.8). Manual draws in the terminal's own
// foreground, and so does a name this build has no token for: a colour would
// say something about a mode nothing here knows anything about.
func (s status) tint(name string, palette styles.Palette) string {
	switch permission.Mode(name) {
	case permission.ModePlan:
		return palette.ModePlan.Render(name)
	case permission.ModeAuto:
		return palette.ModeAuto.Render(name)
	}
	return name
}

func lineWidth(segments []string) int {
	width := ansi.StringWidth(statusSep) * (len(segments) - 1)
	for _, segment := range segments {
		width += ansi.StringWidth(segment)
	}
	return width
}

// tokens is a count as this line says it, rounded past a thousand because the
// columns a six-figure count spends are ones the segments beside it need more.
func tokens(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	// The boundary is where one decimal of thousands rounds to 1000.0, not the
	// million: past it, "1000k" is a megatoken spelled the long way.
	case n < 999_950:
		return scaled(n, 1000) + "k"
	}
	return scaled(n, 1_000_000) + "M"
}

func scaled(n, unit int) string {
	return strings.TrimSuffix(strconv.FormatFloat(float64(n)/float64(unit), 'f', 1, 64), ".0")
}

// sent is the prompt one call carried. Three fields because llm.Usage.Input
// excludes whatever the cache served or wrote, so Input alone reads as a
// fraction of the real prompt on any session the cache is working on.
func sent(u llm.Usage) int { return u.Input + u.CacheRead + u.CacheWrite }

func addUsage(a, b llm.Usage) llm.Usage {
	return llm.Usage{
		Input:      a.Input + b.Input,
		Output:     a.Output + b.Output,
		CacheRead:  a.CacheRead + b.CacheRead,
		CacheWrite: a.CacheWrite + b.CacheWrite,
	}
}
