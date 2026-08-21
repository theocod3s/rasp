package tui

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

const (
	statusSep = " · "

	// yoloBadge stands where the mode name would be, rather than beside it: under
	// yolo no preset is consulted at all (design §7.7), so a line still reading
	// "plan" would name rules nothing is running.
	yoloBadge = "⚡ YOLO"

	// costSegment is the one thing here nothing can fill in: a price is per model
	// and comes from the catalog (design §10.2), which nothing fetches yet. A
	// table written here instead would put a wrong number where a reader looks
	// for the bill, and that is worse than an absent one.
	costSegment = "cost —"
)

// yoloStyle is inverse video rather than a palette token, and deliberately not
// in styles.Palette: swapping the terminal's own foreground and background is
// the one styling no theme can wash out, which is what design §7.8 asks of a
// badge that has to stay loud for as long as it is on the screen. A colour would
// have to be legible on a background nothing here has been told about.
var yoloStyle = lipgloss.NewStyle().Reverse(true).Bold(true)

// status is the line between the conversation and the input. View draws it from
// the model's own fields rather than as an item in the conversation, which is
// what leaves a frame that moved only these numbers costing no item render at
// all (internals §4.5).
type status struct {
	model string
	mode  permission.Mode

	// yolo is whether the bypass ahead of the ladder is armed. Separate from mode
	// because it is not one: the mode underneath keeps standing, and is what the
	// session goes back to when the bypass is turned off (yolo.go).
	yolo bool

	// context is what the last model call accounted for, prompt and reply
	// together — a floor for the next request rather than the whole of it, since
	// the tool results and prompts added since are estimated off the transcript
	// (design §11), which the UI does not hold. Not a share of the window: that
	// size comes from the catalog, along with the price.
	context int

	// spent is what the finished turns cost, running the turn in flight, and step
	// the step still streaming. Three fields because each is replaced by the one
	// after it rather than added to it: a turn's end brings the loop's own total
	// (agent.Event.Usage) over the same messages running was built from, and a
	// step's end brings the final counts for the message step has been reporting
	// a growing prefix of.
	spent, running, step llm.Usage
}

// streaming is what the step has reported so far, read off the accumulated
// message a delta carries. Those counts are cumulative for the step, so this
// replaces rather than adds.
//
// A report of nothing is left alone rather than written down: Anthropic sends
// the input count at message_start and the output count in one message_delta at
// the end, and an OpenAI-compatible endpoint sends the lot in a final chunk — so
// a zero here means the provider has not said yet, and taking it at face value
// would blank a line that was reading correctly a moment ago.
func (s status) streaming(u llm.Usage) status {
	if u == (llm.Usage{}) {
		return s
	}
	s.step = u
	s.context = sent(u) + u.Output
	return s
}

func (s status) call(u llm.Usage) status {
	s.context = sent(u) + u.Output
	s.running = addUsage(s.running, u)
	s.step = llm.Usage{}
	return s
}

func (s status) turnEnded(u llm.Usage) status {
	s.spent = addUsage(s.spent, u)
	s.running = llm.Usage{}
	s.step = llm.Usage{}
	return s
}

// Render draws the line at width, dropping whole segments from the right until
// it fits — elided, half a token count reads as a smaller one. What is guarding
// the session comes first and survives every drop.
func (s status) Render(width int, bg styles.Background) string {
	palette := styles.For(bg)
	total := addUsage(addUsage(s.spent, s.running), s.step)

	segments := []string{s.head()}
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

	head := s.tint(segments[0], palette)
	if len(segments) == 1 {
		// The one place anything is cut. A terminal too narrow even for the mode
		// says as much of it as fits, rather than dropping the line entirely on
		// the screens with least room to work out the mode from anything else.
		if width > 0 && ansi.StringWidth(head) > width {
			return ansi.Truncate(head, width, "")
		}
		return head
	}
	return head + palette.Muted.Render(statusSep+strings.Join(segments[1:], statusSep))
}

// head is the first segment: the mode the session is gated by, or the badge
// saying nothing is.
func (s status) head() string {
	if s.yolo {
		return yoloBadge
	}
	return string(s.modeName())
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

// tint colours the first segment (design §7.8). Manual draws in the terminal's
// own foreground, and so does a name this build has no token for: a colour would
// say something about a mode nothing here knows anything about.
func (s status) tint(name string, palette styles.Palette) string {
	if s.yolo {
		return yoloStyle.Render(name)
	}
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
