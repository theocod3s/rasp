package chat_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tui/chat"
)

// TestAFinishedItemIsRenderedOnceAndThenFrozen is internals §4.5's freeze: a
// message that has stopped changing is drawn once at a width and handed back
// verbatim for every frame after, without Render being called at all.
// A reply that thought first is drawn here too: the thinking is a second
// segment inside the same item, so it is the same freeze — and a segment that
// went on re-rendering would be one more thing moving on every blink.
func TestAFinishedItemIsRenderedOnceAndThenFrozen(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply llm.Message
	}{
		{"a reply", reply("Reading it now.")},
		{"a reply that thought first", thoughtful(theThought, "Reading it now.")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var runs int
			var v chat.View
			v.Append(item(&runs, tc.reply, true))

			first := v.Render(80)
			for range 5 {
				if got := v.Render(80); got != first {
					t.Fatalf("a later frame reads %q, and the item was frozen at %q", got, first)
				}
			}

			if runs != 1 {
				t.Errorf("the item rendered %d time(s) across six frames at one width, want 1", runs)
			}
		})
	}
}

// TestAnItemStillArrivingRendersEveryFrame is the other half, and the reason
// the count above means anything: a view that never rendered anything would
// pass that test too.
func TestAnItemStillArrivingRendersEveryFrame(t *testing.T) {
	var runs int
	var v chat.View
	v.Set("reply", said(&runs, "Reading it n", false))

	for range 3 {
		v.Render(80)
	}

	if runs != 3 {
		t.Errorf("the item rendered %d time(s) across three frames; one still arriving has no final "+
			"form to freeze, so every frame is a real render", runs)
	}
}

// TestAResizeRendersAgainRatherThanServingTheOldWidth. The cache is keyed by
// width and holds one, so a terminal that changed size gets the item drawn for
// the size it is now — the failure this prevents is a frozen string wrapped for
// a width the terminal no longer has.
func TestAResizeRendersAgainRatherThanServingTheOldWidth(t *testing.T) {
	var runs int
	var v chat.View
	v.Append(said(&runs, "The failing test asserts the header is parsed before the body.", true))

	wide := v.Render(60)
	narrow := v.Render(20)

	if narrow == wide {
		t.Fatal("the item draws the same at 60 columns and at 20, so nothing here can tell a stale " +
			"cache from a fresh render")
	}

	var fresh chat.View
	fresh.Append(said(new(int), "The failing test asserts the header is parsed before the body.", true))
	if want := fresh.Render(20); narrow != want {
		t.Errorf("after the resize the view drew\n%q\nand the item at 20 columns is\n%q", narrow, want)
	}
	if back := v.Render(60); back != wide {
		t.Errorf("back at 60 columns the view drew\n%q\nand it drew\n%q\nbefore the resize", back, wide)
	}

	if runs != 3 {
		t.Errorf("the item rendered %d time(s) across three widths-changed frames, want one each", runs)
	}
}

// TestReplacingAFrozenItemDrawsTheNewOne. The freeze belongs to the item, not to
// the key: something put under a key already holding a finished item is drawn.
func TestReplacingAFrozenItemDrawsTheNewOne(t *testing.T) {
	var v chat.View
	v.Set("call", chat.Call{Name: "read", State: chat.CallDone})
	v.Render(80)

	v.Set("call", chat.Call{Name: "read", State: chat.CallDone, Result: &tool.Result{IsError: true}})

	if got := v.Render(80); !strings.Contains(got, "read failed") {
		t.Errorf("the frame reads %q; the freeze outlived the item it was taken from", got)
	}
}

// TestSetKeepsAnItemWhereItFirstAppeared: a tool call that finishes has to stay
// beside the reply that asked for it, not jump to the end of the conversation.
func TestSetKeepsAnItemWhereItFirstAppeared(t *testing.T) {
	var v chat.View
	v.Set("first", chat.Call{Name: "read", State: chat.CallRunning})
	v.Set("second", chat.Call{Name: "write", State: chat.CallRunning})
	v.Set("first", chat.Call{Name: "read", State: chat.CallDone})

	if got, want := v.Render(80), "  read done\n  write running\n"; got != want {
		t.Errorf("the conversation reads\n%q\nwant\n%q", got, want)
	}
	if v.Len() != 2 {
		t.Errorf("the conversation holds %d items; the second Set replaced one rather than adding it", v.Len())
	}
}

// TestAnItemThatDrawsNothingTakesNoLine. A step that only asked for tools has an
// assistant message with no text in it, and a blank line for every such step is
// a transcript full of holes.
func TestAnItemThatDrawsNothingTakesNoLine(t *testing.T) {
	var v chat.View
	v.Append(chat.Message{
		Content: llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.Block{{Type: llm.BlockToolUse, Name: "read"}},
		},
		Done: true,
	})
	v.Append(said(new(int), "Done.", true))

	if got, want := words(v.Render(0)), "Done."; got != want {
		t.Errorf("the conversation reads %q, want %q", got, want)
	}
}

func TestAMessageDrawsOnlyWhatAReaderIsMeantToSee(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message llm.Message
		want    string
	}{
		{
			name: "a prompt is marked as the user's",
			message: llm.Message{
				Role:    llm.RoleUser,
				Content: []llm.Block{{Type: llm.BlockText, Text: "fix the auth test"}},
			},
			want: chat.Caret + "fix the auth test",
		},
		{
			name: "thinking is drawn above the reply",
			message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.Block{
					{Type: llm.BlockThinking, Text: "the header is parsed twice"},
					{Type: llm.BlockText, Text: "Found it."},
				},
			},
			want: "the header is parsed twice Found it.",
		},
		{
			name: "a tool call is an item of its own",
			message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.Block{
					{Type: llm.BlockText, Text: "Reading it."},
					{Type: llm.BlockToolUse, ID: "call_1", Name: "read"},
				},
			},
			want: "Reading it.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := words((chat.Message{Content: tc.message, Done: true}).Render(0)); got != tc.want {
				t.Errorf("the message draws %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAReplyIsMarkdownAndAPromptIsNot. The model writes documents and the user
// writes a line they mean literally, so only one of the two is a renderer's
// input.
func TestAReplyIsMarkdownAndAPromptIsNot(t *testing.T) {
	const heading = "# the header is parsed twice"

	if drawn := words((chat.Message{Content: reply(heading), Done: true}).Render(40)); strings.Contains(drawn, "#") {
		t.Errorf("the reply draws %q, and a heading is drawn as one rather than as its hash", drawn)
	}

	typed := chat.Message{
		Content: llm.Message{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockText, Text: heading}}},
		Done:    true,
	}
	if got, want := words(typed.Render(40)), chat.Caret+heading; got != want {
		t.Errorf("the prompt draws %q, want %q", got, want)
	}
}

// words is what a frame says: the escape sequences markdown is styled with
// taken out, and the wrapping and margins squeezed away.
func words(frame string) string {
	return strings.Join(strings.Fields(ansi.Strip(frame)), " ")
}

// BenchmarkCursorBlink is the frame a blinking cursor costs in a 200-message
// conversation: nothing has changed, so no item is drawn at all. The count is
// the assertion — internals §4.5's claim is that this number is zero, and the
// alternative is 200 renders thirty times a second.
func BenchmarkCursorBlink(b *testing.B) {
	const width = 80

	var runs int
	v := conversation(&runs, 200)

	v.Render(width)
	if runs != 200 {
		b.Fatalf("the first frame drew %d of 200 items; the count below rests on every item having "+
			"been drawn once already", runs)
	}
	warm := runs

	for b.Loop() {
		v.Render(width)
	}

	if blinked := runs - warm; blinked != 0 {
		b.Fatalf("%d item render(s) across %d frames in which nothing changed", blinked, b.N)
	}
	b.ReportMetric(float64(runs-warm)/float64(b.N), "renders/op")
}

// BenchmarkResizingFrame is the same conversation with the width moving under
// it, so every item misses on every frame. It is what a blink would cost with no
// cache at all, over the same 200 items.
func BenchmarkResizingFrame(b *testing.B) {
	var runs int
	v := conversation(&runs, 200)

	width := 80
	for b.Loop() {
		width ^= 1
		v.Render(width)
	}

	if runs < b.N*200 {
		b.Fatalf("%d item render(s) across %d frames of 200 items; a width that changed every frame "+
			"should have missed every time", runs, b.N)
	}
	b.ReportMetric(float64(runs)/float64(b.N), "renders/op")
}

// paragraph is long enough that wrapping it is real work, which is what makes
// the two benchmarks above comparable.
const paragraph = "Reading the file now, and the two it imports. The failing test asserts that the " +
	"header is parsed before the body, and the parser reorders them whenever the body arrives first."

// conversation is n finished messages, every other one of them a reply that
// thought before it spoke — so what a blink costs is measured over the segment
// thinking adds as well as over the reply.
func conversation(runs *int, n int) chat.View {
	var v chat.View
	for i := range n {
		text := fmt.Sprintf("%d. %s", i, paragraph)
		msg := reply(text)
		if i%2 == 1 {
			msg = thoughtful(theThought, text)
		}
		v.Append(item(runs, msg, true))
	}
	return v
}

// counted renders exactly as a Message does and records how often it was asked
// to. From outside the package a frame that reused a cached string and one that
// rebuilt it are otherwise identical, which is the whole thing under test.
type counted struct {
	chat.Message
	runs *int
}

func (c counted) Render(width int) string {
	*c.runs++
	return c.Message.Render(width)
}

func said(runs *int, text string, done bool) counted { return item(runs, reply(text), done) }

func item(runs *int, msg llm.Message, done bool) counted {
	return counted{Message: chat.Message{Content: msg, Done: done}, runs: runs}
}

func reply(text string) llm.Message {
	return llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.Block{{Type: llm.BlockText, Text: text}},
	}
}
