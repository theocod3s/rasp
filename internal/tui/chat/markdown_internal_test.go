package chat

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// corpus is a set of replies of the shape an assistant actually sends, chosen
// for the constructs whose meaning is not settled until a later line arrives.
var corpus = map[string]string{
	"fenced code between prose":  "Here's the fix:\n\n```go\nfunc Check(ctx context.Context) error {\n\treturn nil\n}\n```\n\nNow the caller.\n",
	"headings and a setext one":  "Result\n======\n\nThe parse succeeded.\n\n## Next\n\nRun it again.\n",
	"a list that turns loose":    "Two things:\n\n- the header\n- the body\n\n- and the trailer, which arrived late\n\nThat is all.\n",
	"an ordered list":            "Steps:\n\n1. read the file\n2. edit it\n3. run the tests\n\nDone.\n",
	"a table":                    "| field | kind |\n|---|---|\n| id | int |\n| name | string |\n\nAfter the table.\n",
	"a quote then prose":         "> The parser reorders them.\n> Every time.\n\nSo the fix is upstream.\n",
	"indented code with a gap":   "Like so:\n\n    first line\n\n    second line\n\nBack to prose.\n",
	"pipes and backticks":        "Run `ls | wc -l` first. A pipe in prose | is not a table.\n\nThen `sed -n '1,5p'`.\n",
	"a link reference":           "See [the notes][n] for why.\n\n[n]: https://example.invalid/notes\n\nAnd the fix.\n",
	"an html block with a gap":   "<details>\n<summary>trace</summary>\n\nthe stack\n\n</details>\n\nAfter it.\n",
	"nested list and code":       "- outer\n  - inner\n\n    ```sh\n    go test ./...\n    ```\n\n- second\n\nEnd.\n",
	"a fence inside a list item": "1. read it\n\n   ```sh\n   cat main.go\n   ```\n\n2. edit it\n\nEnd.\n",
}

// TestEveryPrefixOfAReplyRendersAsThoughItWereWhole is the boundary detector's
// proof, checked rather than argued: at every length the message could be
// caught at, the split rendering has to put the same characters in the same
// places as one render of the same text. A prefix is fed to the same renderer
// the frame before it used, so the memo is under test too — a boundary that
// moved wrongly two deltas ago shows up here.
func TestEveryPrefixOfAReplyRendersAsThoughItWereWhole(t *testing.T) {
	const width = 60
	for name, doc := range corpus {
		t.Run(name, func(t *testing.T) {
			m := &markdown{draw: glamourBlock}
			for n := 1; n <= len(doc); n++ {
				got := visible(m.render(doc[:n], width))
				want := visible(glamourBlock(doc[:n], width))
				if got != want {
					t.Fatalf("the first %d bytes of the reply draw\n%s\nand one render of the same text draws\n%s",
						n, indent(got), indent(want))
				}
			}
		})
	}
}

// aloud is the thinking hung above every reply in the two tests below. Two
// paragraphs and a fence, so a segment that did reach the renderer would render
// as something rather than come back looking like the plain text it is.
const aloud = "The header is parsed twice.\n\n```go\nclaims, err := parse(h)\n```\n\nSo the fix is upstream."

// TestThinkingNeverReachesTheMarkdownRenderer is the claim in its bluntest
// form, checked at the one place it can be: what the renderer is handed.
func TestThinkingNeverReachesTheMarkdownRenderer(t *testing.T) {
	const width = 60

	var seen []string
	renderThrough(t, &markdown{draw: func(src string, width int) string {
		seen = append(seen, src)
		return glamourBlock(src, width)
	}})

	doc := corpus["fenced code between prose"]
	for n := 1; n <= len(doc); n++ {
		reasoning(aloud, doc[:n]).Render(width)
	}

	if len(seen) == 0 {
		t.Fatal("the renderer was never called, so the loop below examined nothing")
	}
	for _, src := range seen {
		if strings.Contains(src, "parsed twice") {
			t.Fatalf("the markdown renderer was handed thinking:\n%q", src)
		}
	}
}

// TestThinkingLeavesTheReplysBoundariesWhereTheyWere is the corpus proof again
// with reasoning hung above every reply. The boundary detector reads the reply
// alone, and this is what holds that true from outside: a thought of any shape
// must leave every prefix rendering exactly as it did without one.
func TestThinkingLeavesTheReplysBoundariesWhereTheyWere(t *testing.T) {
	const width = 60
	head := insetPainted(aloud, styles.For(styles.Dark).Faint, width)

	for name, doc := range corpus {
		t.Run(name, func(t *testing.T) {
			renderThrough(t, &markdown{draw: glamourBlock})

			for n := 1; n <= len(doc); n++ {
				drawn := reasoning(aloud, doc[:n]).Render(width)
				body, ok := strings.CutPrefix(drawn, head)
				if !ok {
					t.Fatalf("the first %d bytes of the reply drew\n%s\nand the thinking above them is\n%s",
						n, indent(drawn), indent(head))
				}
				// The separator only when there is something under it: a prefix
				// glamour draws nothing for — one `>`, half an HTML tag — leaves the
				// thinking standing alone rather than over a blank line.
				got := visible(strings.TrimPrefix(body, "\n\n"))
				want := visible(glamourBlock(doc[:n], width))
				if got != want {
					t.Fatalf("under its thinking, the first %d bytes of the reply draw\n%s\nand one render "+
						"of the same text draws\n%s", n, indent(got), indent(want))
				}
			}
		})
	}
}

// reasoning is a reply that worked through something first, as the conversation
// draws it.
func reasoning(thinking, text string) Message {
	return Message{Content: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockThinking, Text: thinking},
		{Type: llm.BlockText, Text: text},
	}}}
}

// renderThrough swaps the package renderer for the length of one test.
// Message.Render draws through that one rather than through a renderer handed
// to it, so this is the only way to see what it was given.
func renderThrough(t *testing.T, with *markdown) {
	t.Helper()

	was := md
	t.Cleanup(func() { md = was })
	md = with
}

// blocks are the pieces the generated replies below are built from, one of each
// construct that has ever had to be reasoned about — including the ones that
// only go wrong nested inside another.
var blocks = []string{
	"A paragraph about the parser and the header it reads.\n\n",
	"# A heading\n\n",
	"Setext\n======\n\n",
	"```go\nfunc Check(ctx context.Context) error { return nil }\n```\n\n",
	"- one\n- two\n\n",
	"1. first\n2. second\n\n",
	"- item\n\n  ```sh\n  go test ./...\n  ```\n\n",
	"- outer\n  - inner\n    - deepest\n\n",
	"> quoted text\n> and more of it\n\n",
	"| field | kind |\n|---|---|\n| id | int |\n\n",
	"    indented code\n\n    and more of it\n\n",
	"<details>\n<summary>trace</summary>\n\nthe stack\n\n</details>\n\n",
	"See [the notes][n].\n\n[n]: https://example.invalid\n\n",
	"term\n\n: what it means\n\n",
	"Text with `a | b` in it and a - dash.\n\n",
	"***\n\n",
}

// TestBlocksInAnyOrderStillRenderAsOneDocument is the corpus above without the
// hand-picking. Nesting is where the proof has actually been wrong — a fence
// closing inside a list item read as closing the list — and that only shows up
// in a combination nobody thought to write down.
func TestBlocksInAnyOrderStillRenderAsOneDocument(t *testing.T) {
	const width = 60
	rng := rand.New(rand.NewPCG(1, 2))

	for reply := range 24 {
		var b strings.Builder
		for range 5 {
			b.WriteString(blocks[rng.IntN(len(blocks))])
		}
		doc := b.String()

		m := &markdown{draw: glamourBlock}
		for at := strings.IndexByte(doc, '\n'); at >= 0 && at < len(doc); {
			got := visible(m.render(doc[:at+1], width))
			want := visible(glamourBlock(doc[:at+1], width))
			if got != want {
				t.Fatalf("reply %d cut at %d bytes draws\n%s\nand one render of the same text draws\n%s\nfrom\n%q",
					reply, at+1, indent(got), indent(want), doc[:at+1])
			}
			next := strings.IndexByte(doc[at+1:], '\n')
			if next < 0 {
				break
			}
			at += next + 1
		}
	}
}

// TestABoundaryIsOnlyClaimedWhereNothingIsOpen names the constructs the
// detector has to see through. Each case is a message caught mid-construct, and
// the answer that keeps the frame correct is the whole of it (0) or a boundary
// that stops short of where the construct began.
func TestABoundaryIsOnlyClaimedWhereNothingIsOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want int
	}{
		{"nothing has closed yet", "Here's the fix", 0},
		{"one paragraph and a blank line", "Here's the fix\n\n", 16},
		{"a fence still open", "Fixed it:\n\n```go\nfunc Check() {\n\n", 11},
		{"a fence that closed", "Fixed it:\n\n```go\nfunc Check() {}\n```\n\nNow the caller", 38},
		{"a list that may still turn loose", "Two:\n\n- one\n\n", 6},
		{"a fence closing inside a list item", "1. read it\n\n   ```sh\n   cat main.go\n   ```\n\n", 0},
		{"a list closed by a paragraph", "Two:\n\n- one\n- two\n\ntext\n\n", 25},
		{"a table mid-row", "| a | b |\n|---|---|\n| 1 | 2 |\n\n", 0},
		{"a quote", "> quoted\n\n", 0},
		{"a paragraph that may become a setext header", "Result\n", 0},
		{"indented code that may resume", "Like so:\n\n    code\n\n", 10},
		{"a link reference anywhere in the message", "text\n\nmore\n\n[n]: https://example.invalid\n\n", 0},
		{"a definition list marker", "term\n\n: what it means\n\n", 0},
		{"raw html anywhere in the message", "text\n\n<details>\n\nthe stack\n\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stableBoundary(tc.src); got != tc.want {
				t.Errorf("the detector proves %d of %d bytes final, want %d\nsafe: %q\nrest: %q",
					got, len(tc.src), tc.want, tc.src[:got], tc.src[got:])
			}
		})
	}
}

// TestAFrameCostsTheTailRatherThanTheReply is the point of the whole file. What
// a delta costs is measured at two reply lengths rather than against a
// threshold, because the claim is about a shape and not a number: double the
// reply and a frame costs what it did, where rendering the accumulation every
// frame costs twice as much (internals §4.4).
func TestAFrameCostsTheTailRatherThanTheReply(t *testing.T) {
	short, long := streamed(t, 8), streamed(t, 16)

	if long.reply != 2*short.reply {
		t.Fatalf("the two replies are %d and %d bytes, and every line below reads them as one doubling "+
			"the other", short.reply, long.reply)
	}
	// The control. Without it a renderer that drew nothing at all would pass
	// every line below.
	if short.whole == 0 || long.whole <= short.whole {
		t.Fatalf("the long reply handed glamour %d bytes and the short one %d, so nothing here "+
			"measured a stream growing", long.whole, short.whole)
	}
	if long.frame > 6*short.frame/5 {
		t.Errorf("a frame of the %d-byte reply hands glamour %d bytes on average and a frame of the "+
			"%d-byte one hands it %d; doubling the reply has to leave that flat",
			long.reply, long.frame, short.reply, short.frame)
	}
	if long.worst > 3*len(block) {
		t.Errorf("the costliest frame of the %d-byte reply handed glamour %d bytes, and a bounded tail "+
			"cannot cost more than a few blocks", long.reply, long.worst)
	}

	naive := long.reply * long.reply / (2 * step)
	t.Logf("%d-byte reply: %d bytes drawn in total against %d for a whole render per delta, "+
		"%d per frame, %d in the costliest", long.reply, long.whole, naive, long.frame, long.worst)
}

const (
	block = "A paragraph about the parser and the header it reads.\n\n" +
		"```go\nfunc Check(ctx context.Context) error { return nil }\n```\n\n"
	step = 7 // bytes per delta, small enough that a frame's cost is the tail's and nothing else
)

type stream struct {
	reply, whole, frame, worst int
}

// streamed replays a reply of n blocks delta by delta and totals what reached
// glamour.
func streamed(t *testing.T, n int) stream {
	t.Helper()
	const width = 60

	var frame int
	m := &markdown{draw: func(src string, width int) string {
		frame += len(src)
		return glamourBlock(src, width)
	}}

	reply := strings.Repeat(block, n)
	got := stream{reply: len(reply)}
	frames := 0
	for at := 1; at <= len(reply); at += step {
		frame = 0
		m.render(reply[:at], width)
		got.whole += frame
		got.worst = max(got.worst, frame)
		frames++
	}
	got.frame = got.whole / frames
	return got
}

// TestAWidthChangeDropsTheHeadRatherThanServingIt. The memo holds one width; a
// terminal that resized has to be drawn for the size it is now, and the head is
// the half of the message a stale entry would hide.
func TestAWidthChangeDropsTheHeadRatherThanServingIt(t *testing.T) {
	doc := corpus["fenced code between prose"]
	m := &markdown{draw: glamourBlock}

	m.render(doc, 80)
	narrow := m.render(doc, 30)

	if want := visible(glamourBlock(doc, 30)); visible(narrow) != want {
		t.Errorf("after the resize the reply draws\n%s\nand one render at 30 columns draws\n%s",
			indent(visible(narrow)), indent(want))
	}
}

// TestAReplyDoesNotInheritTheHeadOfTheOneBeforeIt. The memo holds one entry and
// the conversation has more than one message in it — a reply that has just
// finished is drawn once more before it freezes, in the same frame as the one
// still arriving. What makes that safe is the entry being keyed by the bytes it
// was taken from; a message that merely got there second must not be handed the
// other's head.
func TestAReplyDoesNotInheritTheHeadOfTheOneBeforeIt(t *testing.T) {
	const width = 60
	const first, second = "Done.\n\n", "The header is parsed before the body.\n\nAnd the parser reorders them.\n\n"

	if n := stableBoundary(second); n < len(first) {
		t.Fatalf("the second reply proves %d bytes final and the first proves %d, so the memo would be "+
			"dropped for its length alone and this test asserts nothing", n, len(first))
	}

	m := &markdown{draw: glamourBlock}
	m.render(first, width)

	if got, want := visible(m.render(second, width)), visible(glamourBlock(second, width)); got != want {
		t.Errorf("the second reply draws\n%s\nand on its own it draws\n%s", indent(got), indent(want))
	}
}

// TestARendererIsSharedAcrossFrames guards the one thing the package-level
// renderer exists for. Glamour is not reentrant, so the two calls a frame makes
// are serialized; this is what fails under -race if that lock ever goes.
func TestARendererIsSharedAcrossFrames(t *testing.T) {
	doc := corpus["a table"]
	done := make(chan string, 2)
	for range 2 {
		go func() { done <- md.render(doc, 60) }()
	}
	if a, b := <-done, <-done; a != b {
		t.Errorf("two frames of the same reply drew different text:\n%s\nand\n%s", indent(a), indent(b))
	}
}

// TestGlamourRendererIsMemoizedPerWidth is the memo's own proof: the same
// width has to return the same *TermRenderer, and a different width must not
// share it.
func TestGlamourRendererIsMemoizedPerWidth(t *testing.T) {
	renderersMu.Lock()
	defer renderersMu.Unlock()

	a, err := renderer(60)
	if err != nil {
		t.Fatalf("renderer(60): %v", err)
	}
	again, err := renderer(60)
	if err != nil {
		t.Fatalf("renderer(60): %v", err)
	}
	if a != again {
		t.Error("two calls at the same width built two renderers instead of reusing one")
	}

	other, err := renderer(40)
	if err != nil {
		t.Fatalf("renderer(40): %v", err)
	}
	if a == other {
		t.Error("two different widths shared one renderer")
	}
}

// TestGlamourBlockIsSafeForConcurrentCallers exists to fail under -race: once
// a renderer is memoized, callers at the same width share it, and glamour is
// not reentrant. Direct calls rather than through markdown.render, which
// already serializes on its own mutex and would prove nothing about this one.
func TestGlamourBlockIsSafeForConcurrentCallers(t *testing.T) {
	doc := corpus["a table"]
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			glamourBlock(doc, 60)
		}()
	}
	wg.Wait()
}

// BenchmarkStreamingReply replays one long reply delta by delta, the way a turn
// delivers it. The metric is what a frame costs on average; without the
// boundary it climbs with every delta, because every delta re-renders the
// accumulation.
func BenchmarkStreamingReply(b *testing.B) {
	const width = 60
	reply := strings.Repeat("A paragraph about the parser and the header it reads.\n\n"+
		"```go\nfunc Check(ctx context.Context) error { return nil }\n```\n\n", 20)

	for _, tc := range []struct {
		name   string
		render func(m *markdown, src string)
	}{
		{"stable prefix", func(m *markdown, src string) { m.render(src, width) }},
		{"whole reply every delta", func(m *markdown, src string) { glamourBlock(src, width) }},
	} {
		b.Run(tc.name, func(b *testing.B) {
			var frames int
			for b.Loop() {
				m := &markdown{draw: glamourBlock}
				for n := 1; n <= len(reply); n += 64 {
					tc.render(m, reply[:n])
					frames++
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(frames), "ns/frame")
		})
	}
}

// visible is a rendering reduced to what a terminal puts on the screen, so that
// two of them can be compared for the thing under test rather than byte by
// byte. Glamour pads a block out to the wrap width when more document follows
// it and leaves the last one ragged, and it re-states a style at a seam where
// it had already been set — both invisible, and both are exactly what a
// document cut in two differs by.
//
// So two runs of bytes go: the trailing run of spaces and escapes after the
// last glyph on a line, and every escape in a run of them but the last. Nothing
// between glyphs is touched, which is where a mis-cut prefix shows up — a fence
// rendered as prose or a list item that lost its bullet moves text, not colour.
func visible(s string) string {
	var b strings.Builder
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		toks := escapes(line)
		for len(toks) > 0 {
			last := toks[len(toks)-1]
			if !isEscape(last) && strings.TrimLeft(last, " \t") != "" {
				break
			}
			toks = toks[:len(toks)-1]
		}
		for i, tok := range toks {
			if isEscape(tok) && i+1 < len(toks) && isEscape(toks[i+1]) {
				continue
			}
			b.WriteString(tok)
		}
	}
	return b.String()
}

// escapes splits a line into escape sequences and the runs of text between
// them.
func escapes(line string) []string {
	var out []string
	for i := 0; i < len(line); {
		j := i
		if line[i] == escape {
			for j < len(line) && line[j] != 'm' {
				j++
			}
			if j < len(line) {
				j++
			}
		} else {
			for j < len(line) && line[j] != escape {
				j++
			}
		}
		out = append(out, line[i:j])
		i = j
	}
	return out
}

func isEscape(tok string) bool { return tok != "" && tok[0] == escape }

// indent makes a failure readable: rendered markdown is full of escapes, and a
// %q of it is a wall nobody can compare by eye.
func indent(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		fmt.Fprintf(&b, "\t| %s\n", line)
	}
	return b.String()
}
