package chat_test

import (
	"strings"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tui/chat"
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
