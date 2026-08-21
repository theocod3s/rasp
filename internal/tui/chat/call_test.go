package chat_test

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tui/chat"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// wide is a terminal nothing under test wraps at, so a card's line can be
// compared to the sentence it is meant to be.
const wide = 200

// TestACardSaysWhatRanAndHowItWentInOneLine. The collapsed card is the whole of
// what a reader gets for a call they do not open, so each state has to say
// something they can act on rather than a word meaning "something happened".
func TestACardSaysWhatRanAndHowItWentInOneLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		call chat.Call
		want string
	}{
		{
			name: "asked for and not yet started",
			call: chat.Call{Name: "read"},
			want: "read queued",
		},
		{
			name: "in flight, too briefly to be worth timing",
			call: chat.Call{Name: "read", State: chat.CallRunning},
			want: "read running",
		},
		{
			name: "in flight long enough to notice",
			call: chat.Call{Name: "bash", State: chat.CallRunning, Elapsed: 4200 * time.Millisecond},
			want: "bash running 4.2s",
		},
		{
			// read, ls, grep and find each write a title opening with their own name.
			name: "a title already naming its tool is not made to say it twice",
			call: answered("read", &tool.Result{Title: "read auth.go (42 lines)"}),
			want: "read auth.go (42 lines)",
		},
		{
			// edit and write lead with the path, bash with the command.
			name: "a title naming a path is prefixed by the tool that wrote it",
			call: answered("edit", &tool.Result{Title: "auth.go +3 -1"}),
			want: "edit auth.go +3 -1",
		},
		{
			name: "a call that took long enough says how long",
			call: withElapsed(answered("bash", &tool.Result{Title: "go test ./..."}), 12*time.Second),
			want: "bash go test ./... 12s",
		},
		{
			// A failing built-in tool writes no title at all, and its content is the
			// sentence saying why.
			name: "a failure carries the refusal rather than the word failed alone",
			call: answered("edit", &tool.Result{
				IsError: true,
				Content: "Cannot edit missing.go: it has not been read.\nRead it first.",
			}),
			want: "edit failed: Cannot edit missing.go: it has not been read.",
		},
		{
			name: "a tool that answered with nothing at all still says it is over",
			call: answered("todos", &tool.Result{}),
			want: "todos done",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := words(tc.call.Render(wide))
			if !strings.HasSuffix(got, tc.want) {
				t.Errorf("the card reads %q, and should end %q", got, tc.want)
			}
			if strings.Contains(tc.call.Render(wide), "\n") {
				t.Errorf("the collapsed card is more than one line:\n%s", tc.call.Render(wide))
			}
		})
	}
}

// TestAFuzzyEditSaysSoOnTheCollapsedLine is the card reading Result.Details.
// The fact lives nowhere else here: the result below carries no title and no
// content, so a card that ignored Details would draw the same line for an edit
// that matched byte for byte and one that did not — and the file no longer
// reads as the model wrote it.
func TestAFuzzyEditSaysSoOnTheCollapsedLine(t *testing.T) {
	const marker = "whitespace-normalized"

	fuzzy := answered("edit", &tool.Result{Details: &tool.DiffDetails{Path: "auth.go", Fuzzy: true}})
	if got := words(fuzzy.Render(wide)); !strings.Contains(got, marker) {
		t.Errorf("the card reads %q and never mentions %q", got, marker)
	}

	exact := answered("edit", &tool.Result{Details: &tool.DiffDetails{Path: "auth.go"}})
	if got := words(exact.Render(wide)); strings.Contains(got, marker) {
		t.Errorf("the card reads %q for an edit that matched byte for byte", got)
	}
}

// TestAFileChangeOpensAsItsDiff is the acceptance criterion where a reader
// meets it: expanding an edit or a write shows what changed, not the sentence
// the model was handed. The card's own line is the path and a line count, which
// is the whole of what the reference implementations show and the reason this
// exists.
func TestAFileChangeOpensAsItsDiff(t *testing.T) {
	call := opened(answered("edit", &tool.Result{
		Title:   "auth.go +1 -1",
		Content: "Edited auth.go: 1 replacement, +1 -1.",
		Details: &tool.DiffDetails{Path: "auth.go", Additions: 1, Deletions: 1, Unified: unified},
	}))

	body := words(call.Render(wide))
	for _, line := range []string{"- return parse(header)", "+ return r.claims()"} {
		if !strings.Contains(body, line) {
			t.Errorf("the card does not draw %q:\n%s", line, body)
		}
	}
	if !call.HasDiff() {
		t.Error("the card does not report having a diff, so nothing will open it without a keypress")
	}
}

// TestAFailedCallShowsWhyRatherThanItsDiff. Nothing built in returns both today
// — a refusal carries no Details — but a tool that partly applied a change and
// then failed would, and a card that drew the diff would lose the sentence
// saying what went wrong under a headline that only says "failed".
func TestAFailedCallShowsWhyRatherThanItsDiff(t *testing.T) {
	call := opened(answered("edit", &tool.Result{
		IsError: true,
		Content: "Edited auth.go, then could not write middleware.go: permission denied.",
		Details: &tool.DiffDetails{Path: "auth.go", Additions: 1, Deletions: 1, Unified: unified},
	}))

	body := words(call.Render(wide))
	if !strings.Contains(body, "permission denied") {
		t.Errorf("the card drops the reason it failed:\n%s", body)
	}
	if call.HasDiff() {
		t.Error("a failed call reports a diff, so the model would open it in place of the reason")
	}
}

// TestATypedNilInDetailsIsNotFollowed. Details is an any, and a tool with no
// diff to report can leave a (*tool.DiffDetails)(nil) in it — which is not a
// nil any. The assertion succeeds and hands the card a pointer to dereference,
// so a card that only checked ok panics. The loop's guard turns that into an
// error result rather than a dead process, and the reader still loses the card.
func TestATypedNilInDetailsIsNotFollowed(t *testing.T) {
	var missing *tool.DiffDetails

	call := opened(answered("write", &tool.Result{
		Title:   "Created auth.go",
		Content: "Created auth.go (0 bytes).",
		Details: missing,
	}))

	if call.HasDiff() {
		t.Error("a card whose Details holds nothing at all reports having a diff")
	}
	if body := words(call.Render(wide)); !strings.Contains(body, "Created auth.go") {
		t.Errorf("the card drew neither a diff nor what the tool said:\n%s", body)
	}
}

// TestACardWithNoDiffStillShowsWhatTheToolSaid. The diff is a different body,
// not an extra one, so a tool that changed no file must not lose its output to
// the branch that draws diffs.
func TestACardWithNoDiffStillShowsWhatTheToolSaid(t *testing.T) {
	call := opened(answered("bash", &tool.Result{Title: "go test ./...", Content: "ok  rasp/internal/auth"}))

	if body := words(call.Render(wide)); !strings.Contains(body, "ok rasp/internal/auth") {
		t.Errorf("the card lost the tool's output:\n%s", body)
	}
	if call.HasDiff() {
		t.Error("a bash result reports having a diff")
	}
}

// TestAnEditThatChangedNothingFallsBackToItsOutput. A diff with nothing in it
// has two spellings — go-udiff renders no hunks as no text at all, and a
// header pair with nothing under it draws nothing once the header is dropped.
// A card that called either one a diff would open onto a blank line, and would
// be recorded as open while showing no marker.
func TestAnEditThatChangedNothingFallsBackToItsOutput(t *testing.T) {
	for name, unified := range map[string]string{
		"no text at all": "",
		"a header pair":  "--- a/auth.go\n+++ b/auth.go\n",
	} {
		t.Run(name, func(t *testing.T) {
			call := opened(answered("edit", &tool.Result{
				Title:   "auth.go +0 -0",
				Content: "Edited auth.go: 1 replacement, +0 -0.",
				Details: &tool.DiffDetails{Path: "auth.go", Unified: unified},
			}))

			if call.HasDiff() {
				t.Fatal("an empty diff reports as a diff, so the card opens onto nothing")
			}
			if body := words(call.Render(wide)); !strings.Contains(body, "Edited auth.go") {
				t.Errorf("the card shows neither a diff nor what the tool said:\n%s", body)
			}
		})
	}
}

// TestADiffLineIsCutAtTheWidthRatherThanWrapped. The card wraps everything else
// it draws, and a diff line wrapped is one changed line reading as two.
func TestADiffLineIsCutAtTheWidthRatherThanWrapped(t *testing.T) {
	const narrow = 30

	call := opened(answered("edit", &tool.Result{
		Title:   "auth.go +1 -1",
		Details: &tool.DiffDetails{Path: "auth.go", Unified: unified},
	}))

	// One row for the card's own summary and one per line of the diff the
	// renderer draws, which is the three below its headers — and every one of
	// them has to fit the terminal it was drawn for, or the terminal does the
	// wrapping this is here to avoid. The row count alone would not notice: the
	// card's own indent arithmetic can be off by two with all four rows still
	// present.
	rows := strings.Split(call.Render(narrow), "\n")
	if want := 4; len(rows) != want {
		t.Fatalf("the card drew %d rows, want %d — a diff line wrapped:\n%s",
			len(rows), want, call.Render(narrow))
	}
	for _, row := range rows {
		if n := ansi.StringWidth(row); n > narrow {
			t.Errorf("a row runs %d columns into a terminal %d wide: %q", n, narrow, words(row))
		}
	}
}

// TestACardWhoseOutputIsOnlyWhitespaceDoesNotOfferToOpen. The marker is the
// only promise a card makes about what is under it. Content that survives the
// emptiness test and is then wrapped away to nothing turns that promise into a
// blank line.
func TestACardWhoseOutputIsOnlyWhitespaceDoesNotOfferToOpen(t *testing.T) {
	for name, content := range map[string]string{
		"spaces":            "   ",
		"a padded newline":  "   \n   ",
		"tabs and newlines": "\t\n\t\n",
	} {
		t.Run(name, func(t *testing.T) {
			call := answered("bash", &tool.Result{Title: "go test ./...", Content: content})

			if got := call.Render(wide); !strings.HasPrefix(got, "  ") || strings.HasPrefix(got, "▸") {
				t.Errorf("the card offers to open onto whitespace: %q", got)
			}
			if got := opened(call).Render(wide); strings.Contains(got, "\n") {
				t.Errorf("the card opened onto a blank line: %q", got)
			}
		})
	}
}

// TestLeadingWhitespaceIsPartOfTheOutput. A card draws what a tool printed, and
// for a column of right-aligned numbers the spaces before the first one are the
// alignment. Trimming them takes the top row out of line with every row under
// it — and does it exactly where the eye starts.
func TestLeadingWhitespaceIsPartOfTheOutput(t *testing.T) {
	const output = "   4 auth.go\n  42 middleware.go\n 100 handler.go"

	// The two columns every card body is set in by, spelled out because the
	// card's own constant is unexported.
	const indent = "  "

	call := opened(answered("bash", &tool.Result{Title: "wc -l *.go", Content: output}))

	body := strings.SplitN(call.Render(wide), "\n", 2)[1]
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(body, indent+line) {
			t.Errorf("the card does not draw %q as it was printed:\n%s", line, body)
		}
	}
}

// TestANarrowTerminalDoesNotShredATextBody is the diff's own narrow-width rule
// read across to everything else a card draws. The diff is cut, so it needs a
// column to cut to; text is wrapped, and wrapping to one column is one
// character per row — the failure the summary line is already spared.
func TestANarrowTerminalDoesNotShredATextBody(t *testing.T) {
	call := opened(answered("bash", &tool.Result{
		Title:   "go test ./...",
		Content: "ok github.com/theocod3s/rasp/internal/auth",
	}))

	for width := 1; width <= 2; width++ {
		if rows := strings.Split(call.Render(width), "\n"); len(rows) > 2 {
			t.Errorf("at width %d the output was broken into %d rows: %q",
				width, len(rows)-1, words(call.Render(width)))
		}
	}
}

// TestATerminalTooNarrowForTheIndentStillCuts. A width of zero or less means
// "nobody has said how wide the terminal is" all the way down, and a card that
// subtracted its indent could hand that same number down for a real terminal
// with nothing left — every diff line then goes out uncut and wraps, which is
// the one thing the cut exists to stop, arriving through the sentinel meant to
// help.
func TestATerminalTooNarrowForTheIndentStillCuts(t *testing.T) {
	call := opened(answered("edit", &tool.Result{
		Title:   "auth.go +1 -1",
		Details: &tool.DiffDetails{Path: "auth.go", Unified: unified},
	}))

	// From three, which is the first width where the indent and a column of
	// content both fit. Below it no card is drawable — the indent alone is two
	// columns — and that is the card's own layout rather than the cut.
	for width := 3; width <= 8; width++ {
		for _, row := range strings.Split(call.Render(width), "\n") {
			if n := ansi.StringWidth(row); n > width {
				t.Errorf("at width %d a row measures %d: %q", width, n, words(row))
			}
		}
	}

	// And at the widths no card fits, the diff rows are still cut: what must not
	// happen is a diff line going out at its full length because a real width
	// was read as one nobody had reported. The tolerance is the card's own
	// two-column indent and nothing else — the summary row is exempt, since at
	// these widths it is left whole deliberately.
	const indent = 2
	for width := 1; width <= 2; width++ {
		rows := strings.Split(call.Render(width), "\n")
		for _, row := range rows[1:] {
			if n := ansi.StringWidth(row); n > width+indent {
				t.Errorf("at width %d a diff row measures %d, so it was never cut: %q", width, n, words(row))
			}
		}

		// And the summary stays one row. Clamping it to a column instead breaks
		// it into one character per line, so a terminal that could show one bad
		// row shows eighteen.
		if head := strings.SplitN(call.Render(width), "\n", 2)[0]; strings.Contains(words(head), " e d i t") {
			t.Errorf("at width %d the summary was broken up one character per row: %q", width, words(head))
		}
		if n := len(rows) - 1; n != 3 {
			t.Errorf("at width %d the card drew %d rows under its summary, want the diff's 3", width, n)
		}
	}
}

// TestTheTerminalsBackgroundPicksTheDiffsColours. Nothing else can check this:
// the palette is chosen by a query the terminal answers, and a test has no
// terminal, so the card is asked for both and the two are compared.
func TestTheTerminalsBackgroundPicksTheDiffsColours(t *testing.T) {
	change := &tool.Result{Title: "auth.go +1 -1", Details: &tool.DiffDetails{Path: "auth.go", Unified: unified}}

	onDark := opened(answered("edit", change))
	onLight := onDark
	onLight.Background = styles.Light

	dark, light := onDark.Render(wide), onLight.Render(wide)
	if dark == light {
		t.Fatal("the card drew the same bytes on a light terminal and a dark one")
	}
	if words(dark) != words(light) {
		t.Errorf("the two backgrounds changed the text and not only the colours:\n%s\n\n%s",
			words(dark), words(light))
	}
}

// unified is what go-udiff writes for a one-line replacement.
const unified = "--- a/auth.go\n" +
	"+++ b/auth.go\n" +
	"@@ -8,3 +8,3 @@\n" +
	" func (m *Middleware) claims(r *http.Request) (Claims, error) {\n" +
	"-\treturn parse(header)\n" +
	"+\treturn r.claims()\n"

// TestADetailsPayloadTheCardCannotNameIsLeftAlone. An MCP server's structured
// output arrives as arbitrary decoded JSON in the same field, so the type
// switch has to fall through rather than assume a shape.
func TestADetailsPayloadTheCardCannotNameIsLeftAlone(t *testing.T) {
	call := answered("weather", &tool.Result{
		Title:   "Lisbon, 31.5°C",
		Details: map[string]any{"temperature": 31.5, "city": "Lisbon"},
	})

	if got, want := words(call.Render(wide)), "weather Lisbon, 31.5°C"; got != want {
		t.Errorf("the card reads %q, want %q", got, want)
	}
}

// TestExpandingACardShowsTheToolsOutput, which is the half of a card a reader
// asks for: the summary says a file was read and the body is what was in it.
func TestExpandingACardShowsTheToolsOutput(t *testing.T) {
	const output = "1\tpackage auth\n2\t\n3\tfunc Parse() {}"

	call := answered("read", &tool.Result{Title: "read auth.go (3 lines)", Content: output})

	collapsed := call.Render(wide)
	if strings.Contains(collapsed, "package auth") {
		t.Errorf("the collapsed card already holds the output:\n%s", collapsed)
	}

	call.Expanded = true
	expanded := call.Render(wide)
	for _, line := range strings.Split(output, "\n") {
		if line != "" && !strings.Contains(expanded, line) {
			t.Errorf("the expanded card is missing %q:\n%s", line, expanded)
		}
	}
	if head := strings.SplitN(expanded, "\n", 2)[0]; !strings.Contains(head, "read auth.go (3 lines)") {
		t.Errorf("the expanded card opens %q rather than with its summary", head)
	}
}

// TestOnlyACardWithSomethingUnderItOffersToOpen. The marker is the only thing
// telling a reader a card has more to it, so a running call — which has nothing
// yet — must not wear one.
func TestOnlyACardWithSomethingUnderItOffersToOpen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		call   chat.Call
		marker string
	}{
		{name: "running", call: chat.Call{Name: "bash", State: chat.CallRunning}, marker: "  "},
		{name: "answered with nothing", call: answered("todos", &tool.Result{}), marker: "  "},
		{name: "answered with output", call: answered("read", &tool.Result{Content: "1\tpackage auth"}), marker: "▸ "},
		{
			// The summary line is that whole sentence already.
			name:   "refused in one sentence",
			call:   answered("edit", &tool.Result{IsError: true, Content: "Cannot edit missing.go: it is not there."}),
			marker: "  ",
		},
		{
			name:   "answered with output, opened",
			call:   opened(answered("read", &tool.Result{Content: "1\tpackage auth"})),
			marker: "▾ ",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.call.Render(wide); !strings.HasPrefix(got, tc.marker) {
				t.Errorf("the card opens %q, want %q", got, tc.marker)
			}
		})
	}
}

func answered(name string, res *tool.Result) chat.Call {
	return chat.Call{Name: name, State: chat.CallDone, Result: res}
}

func withElapsed(c chat.Call, d time.Duration) chat.Call {
	c.Elapsed = d
	return c
}

func opened(c chat.Call) chat.Call {
	c.Expanded = true
	return c
}
