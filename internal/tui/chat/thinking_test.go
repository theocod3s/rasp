package chat_test

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tui/chat"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// TestThinkingIsDrawnAboveTheReplyInTheFaintToken. The two segments are one
// item, so the only thing that separates them on screen is their weight — and
// the token is the palette's rather than a colour chosen here, which is what
// puts the contrast under the floor the palette test holds.
func TestThinkingIsDrawnAboveTheReplyInTheFaintToken(t *testing.T) {
	const width = 60

	drawn := chat.Message{Content: thoughtful(theThought, theReply), Done: true}.Render(width)

	head, body, split := strings.Cut(drawn, "\n\n")
	if !split {
		t.Fatalf("the message drew one segment, and thinking is meant to sit above the reply:\n%s", drawn)
	}
	if got := words(head); got != theThought {
		t.Errorf("the faint segment reads\n\t%q\nand the model thought\n\t%q", got, theThought)
	}
	if got := words(body); got != theReply {
		t.Errorf("the reply reads\n\t%q\nand the model said\n\t%q", got, theReply)
	}

	faint := styles.For(styles.Dark).Faint
	for _, line := range strings.Split(head, "\n") {
		if want := faint.Render(ansi.Strip(line)); line != want {
			t.Errorf("a thinking line drew\n\t%q\nand the faint token draws it\n\t%q", line, want)
		}
	}
}

// TestThinkingIsFainterThanTheReplyItIsAbove is the same claim as a measurement,
// and the half the token check cannot make: the reply's weight is glamour's to
// choose, not this palette's, so the only place the two can be compared is the
// frame they both landed in.
func TestThinkingIsFainterThanTheReplyItIsAbove(t *testing.T) {
	const width = 60

	drawn := chat.Message{Content: thoughtful(theThought, theReply), Done: true}.Render(width)
	head, body, split := strings.Cut(drawn, "\n\n")
	if !split {
		t.Fatalf("the message drew one segment, and there is nothing to compare:\n%s", drawn)
	}

	dim, bright := luminance(t, head), luminance(t, body)
	if dim >= bright {
		t.Errorf("thinking is drawn at luminance %.3f and the reply under it at %.3f, so the reader "+
			"has nothing to tell them apart by", dim, bright)
	}
}

// TestThinkingStreamsAndThenStaysWhole. Nothing collapses: the reasoning is
// drawn as it arrives and is still there in full once the reply has landed on
// top of it. Asserted at both ends of the thought rather than at the start of
// it, because a summary line or a first-sentence preview would pass a test that
// only looked for where it began.
func TestThinkingStreamsAndThenStaysWhole(t *testing.T) {
	const width = 60

	long := strings.Repeat(theThought+" ", 12)
	for _, tc := range []struct {
		name string
		item chat.Message
	}{
		{"still arriving", chat.Message{Content: thoughtful(long, "")}},
		{"finished, under its reply", chat.Message{Content: thoughtful(long, theReply), Done: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drawn := words(tc.item.Render(width))
			if want := words(long); !strings.Contains(drawn, want) {
				t.Fatalf("the frame reads\n\t%q\nand the whole thought is\n\t%q", drawn, want)
			}
		})
	}

	// The control: a thought twelve times as long has to draw twelve times as
	// much. Without it, a renderer that had quietly started summarising would
	// still contain every word of the short sample above.
	short := chat.Message{Content: thoughtful(theThought, theReply), Done: true}.Render(width)
	whole := chat.Message{Content: thoughtful(long, theReply), Done: true}.Render(width)
	if len(lines(whole)) <= len(lines(short))+8 {
		t.Errorf("a thought twelve times longer drew %d lines against %d; the segment is being capped",
			len(lines(whole)), len(lines(short)))
	}
}

// TestAMessageWithOnlyThinkingDrawsIt. A model that thinks for ten seconds
// before it says anything is the whole reason this is drawn at all, and an item
// that answers with the empty string takes no line in the conversation (view.go)
// — so the frame would be exactly as silent as it was before.
func TestAMessageWithOnlyThinkingDrawsIt(t *testing.T) {
	item := chat.Message{Content: thoughtful(theThought, "")}

	if got := words(item.Render(60)); got != theThought {
		t.Errorf("a message that has only thought so far draws %q, want %q", got, theThought)
	}
}

const (
	theThought = "The header is parsed twice, once in the middleware and once again in the handler."
	theReply   = "Both call sites now read the parsed header instead of parsing it a second time."
)

// thoughtful is an assistant message that thought before it spoke, with an
// empty reply standing for a step still mid-thought.
func thoughtful(thinking, text string) llm.Message {
	msg := llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.Block{{Type: llm.BlockThinking, Text: thinking}},
	}
	if text != "" {
		msg.Content = append(msg.Content, llm.Block{Type: llm.BlockText, Text: text})
	}
	return msg
}

func lines(drawn string) []string { return strings.Split(strings.TrimSpace(drawn), "\n") }

// luminance is how bright a rendered segment is, as the first foreground colour
// it sets. Relative luminance rather than a colour comparison, because the two
// segments are coloured by different things — the palette above, glamour's own
// style below — and brightness is the only axis they share.
func luminance(t *testing.T, drawn string) float64 {
	t.Helper()

	r, g, b := foreground(t, drawn)
	lin := func(v float64) float64 {
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

// foreground is the first foreground colour a rendered segment sets, in 0..1 per
// channel. A segment that sets none is drawn in whatever the terminal's default
// is, which is not a colour this test can measure — so it says so rather than
// returning a black nothing would ever be fainter than.
func foreground(t *testing.T, drawn string) (r, g, b float64) {
	t.Helper()

	for _, seq := range sgr(drawn) {
		switch {
		case strings.HasPrefix(seq, "38;2;"):
			c := strings.Split(seq[len("38;2;"):], ";")
			if len(c) != 3 {
				continue
			}
			return channel(t, c[0]), channel(t, c[1]), channel(t, c[2])
		case strings.HasPrefix(seq, "38;5;"):
			n := int(channel(t, seq[len("38;5;"):]) * 255)
			if n < 16 {
				t.Fatalf("%q is drawn in one of the sixteen terminal colours, whose brightness is the "+
					"theme's rather than ours; this test cannot measure it", drawn)
			}
			if n >= 232 {
				v := float64(8+10*(n-232)) / 255
				return v, v, v
			}
			n -= 16
			cube := func(i int) float64 {
				if i == 0 {
					return 0
				}
				return float64(55+40*i) / 255
			}
			return cube(n / 36), cube(n / 6 % 6), cube(n % 6)
		}
	}
	t.Fatalf("%q sets no foreground colour, so there is no brightness to compare", drawn)
	return 0, 0, 0
}

// sgr is every escape sequence's parameters, in the order the terminal would
// read them.
func sgr(drawn string) []string {
	var out []string
	for i := 0; i < len(drawn); i++ {
		if drawn[i] != 0x1b || i+1 >= len(drawn) || drawn[i+1] != '[' {
			continue
		}
		end := strings.IndexByte(drawn[i:], 'm')
		if end < 0 {
			break
		}
		out = append(out, drawn[i+2:i+end])
		i += end
	}
	return out
}

func channel(t *testing.T, s string) float64 {
	t.Helper()

	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("reading %q as a colour channel: %v", s, err)
	}
	return float64(n) / 255
}
