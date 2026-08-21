package tui

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tool"
)

// The terminal every frame is recorded at. A reply is wrapped and padded to the
// width it was drawn for, so a golden taken at one size and compared at another
// differs on every line and says nothing about the change that caused it.
const goldenWidth, goldenHeight = 80, 24

// goldenNow is the instant every frame here is drawn at. Any instant will do —
// it is never printed — as long as it is one the machine does not choose.
var goldenNow = time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

// goldenBranch is the branch the footer names in every frame here. Set by hand
// rather than read: a session takes its branch off .git/HEAD under the
// workspace root when it starts (place.go), and goldenConfig names a root no
// machine has — so a frame recording whatever this checkout is on would differ
// on every machine that regenerated it.
const goldenBranch = "main"

// goldenConfig is the session every frame here is drawn for. The default mode,
// so the frames record what a session that configured nothing looks like; the
// two modes with a colour of their own are asserted rather than recorded
// (status_internal_test.go). The depth is one provider's published subset with a
// level already picked, which is what gives the picker a list and a mark to draw.
func goldenConfig() Config {
	return Config{
		Model: "anthropic/claude-opus-5",
		Mode:  permission.ModeManual,
		Depth: &depths{current: llm.EffortHigh, lists: [][]llm.Effort{anthropicish}},

		// Named for TestBannerGolden alone — no other state here renders either
		// field. A path no real machine has for a home directory, so the banner's
		// own ~ abbreviation never fires and the golden stays the same on every
		// machine that records it.
		Version: "v0.2.0",
		Cwd:     "/srv/rasp-demo",
	}
}

// snapshot is one state of the UI worth freezing: the prompt that starts a turn,
// the events the loop then emits into it, the question a gated tool call put to
// the user, and anything they typed after.
type snapshot struct {
	name   string
	prompt string
	events []agent.Event
	ask    *permission.Request
	keys   []tea.KeyPressMsg

	// after moves the fake clock on once the events have landed, which is how a
	// frame gets an animation on it: the spinner glyph and the elapsed times are
	// read off that clock, so naming the instant names the frame.
	after time.Duration
}

// snapshots are the states a golden is kept for. Deliberately few: every ticket
// that changes how the UI draws pays to regenerate all of them, so this holds
// the states that answer a different question about the frame, not every state
// reachable.
func snapshots() []snapshot {
	const prompt = "fix the failing auth test"

	// The two states a step can be caught in before its reply is whole: thinking
	// with nothing said yet, and thinking with the reply arriving under it.
	const aloud = "The test asserts the header is parsed before the body. If the parser reorders " +
		"them, both call sites are reading a header that was parsed twice."
	thinking := thought(aloud, "")
	fragment := thought(aloud, "Reading `auth_test.go` now. The header is parsed")
	explained := spent(asking(reply("Reading `auth_test.go` now. The header is parsed **twice**.\n\n"+
		"- once in the middleware\n- once in the handler\n"),
		llm.Block{Type: llm.BlockToolUse, ID: "call_1", Name: "read"},
		llm.Block{Type: llm.BlockToolUse, ID: "call_2", Name: "edit"}),
		llm.Usage{Input: 812, Output: 143, CacheRead: 11204})
	fixed := spent(reply("Both call sites read the parsed header instead of parsing it again."),
		llm.Usage{Input: 1530, Output: 62, CacheRead: 11204})

	read := &tool.Result{
		Title:   "read auth_test.go (2 lines)",
		Content: "1\tfunc TestParsesTheHeaderOnce(t *testing.T) {\n2\t\treq := request(t)",
	}
	refused := &tool.Result{
		IsError: true,
		Content: "Cannot edit auth.go: it has not been read this session, so old_string would be matched " +
			"against a file rasp has not seen. Read it first.",
	}

	// A turn that changed two files, which is the state the diff renderer exists
	// for. The replacement's second line is deliberately longer than the terminal
	// is wide: a diff line that wrapped would read as two changed lines. Two
	// hunks, and the second one an order of magnitude further down the file, so
	// the frame records what a hunk boundary draws as and what the line numbers
	// line up against.
	changed := asking(reply("Parsing the header once, in the middleware."),
		llm.Block{Type: llm.BlockToolUse, ID: "call_3", Name: "edit"},
		llm.Block{Type: llm.BlockToolUse, ID: "call_4", Name: "write"})
	edited := &tool.Result{
		Title:   "auth.go +3 -2",
		Content: "Edited auth.go: 2 replacements, +3 -2.",
		Details: &tool.DiffDetails{Path: "auth.go", Additions: 3, Deletions: 2, Unified: diff(
			"--- a/auth.go",
			"+++ b/auth.go",
			"@@ -12,5 +12,6 @@",
			" func (m *Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {",
			"-\tclaims, err := parse(r.Header.Get(\"Authorization\"))",
			"+\tclaims, err := m.claims(r)",
			"+\tr = r.WithContext(context.WithValue(r.Context(), claimsKey, claims)) // the handler reads it from here",
			" \tif err != nil {",
			" \t\tunauthorized(w)",
			"@@ -104,3 +105,3 @@",
			" func (m *Middleware) claims(r *http.Request) (Claims, error) {",
			"-\treturn parse(r.Header.Get(\"Authorization\"))",
			"+\treturn parse(r.Header.Get(headerName))",
		)},
	}
	created := &tool.Result{
		Title:   "Created middleware_test.go",
		Content: "Created middleware_test.go (78 bytes).",
		Details: &tool.DiffDetails{Path: "middleware_test.go", Additions: 3, Unified: diff(
			"--- a/middleware_test.go",
			"+++ b/middleware_test.go",
			"@@ -0,0 +1,3 @@",
			"+func TestParsesTheHeaderOnce(t *testing.T) {",
			"+\treq := request(t)",
			"+}",
		)},
	}

	// A batch of two, started and finished in the reverse of the order the model
	// asked for them, which is what a batch running its calls at once does. The
	// cards still have to read in the order of the tool_use blocks above.
	tools := []agent.Event{
		{Kind: agent.EventAssistantDelta, Message: explained},
		{Kind: agent.EventAssistantEnd, Message: explained},
		{Kind: agent.EventToolStart, CallID: "call_2", Tool: "edit"},
		{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"},
		{Kind: agent.EventToolEnd, CallID: "call_2", Tool: "edit", Result: refused},
		{Kind: agent.EventToolEnd, CallID: "call_1", Tool: "read", Result: read},
		{Kind: agent.EventAssistantDelta, Message: fixed},
		{Kind: agent.EventAssistantEnd, Message: fixed},
		// The turn's own total, which the loop sums over the same two messages
		// (agent/step.go) — so the status line ends the turn saying what it said
		// a moment before rather than twice as much.
		{Kind: agent.EventTurnEnd, Usage: llm.Usage{Input: 2342, Output: 205, CacheRead: 22408}},
	}

	return []snapshot{
		{name: "empty"},
		{name: "busy", prompt: prompt},
		// The command list. Recorded rather than only asserted because it is the
		// one place the whole set of commands is visible to a reader, so a command
		// added, renamed or newly able to do its job shows up here as a diff in the
		// words a user will actually read.
		{name: "help", keys: typedLine("/help")},
		// The effort picker. Recorded because the levels are the provider's rather
		// than this package's, so a frame is where a list that stopped following
		// the provider — a rung missing, a mark on the wrong line — is visible as
		// something other than an unchanged green run.
		{name: "effort", keys: typedLine("/effort")},
		// A mode switched from the keyboard. Recorded because it is the one place
		// the words a switch tells the model are visible to a reader, and they are
		// the same words the next turn carries (design §7.5) — so an edit to them
		// shows up here rather than only inside a transcript nobody reads.
		{name: "mode", keys: []tea.KeyPressMsg{shiftTab}},
		// The bypass, reached the only way a session can reach it: the warning the
		// bare /yolo answers with, and then the badge the confirmed one leaves on
		// the status line. Recorded because those are the words standing between a
		// user and every guardrail being off, and because the badge is the whole
		// of what design §7.8 asks the line to do while it is armed.
		{name: "yolo", keys: append(typedLine("/yolo"), typedLine("/yolo "+yoloConfirm)...)},
		// The first Esc against a running turn, which arms rather than cancels
		// (design §6 rule 7) — the arm taking the activity line's hint half is the
		// only thing this state exists to draw.
		{name: "armed", prompt: prompt, keys: []tea.KeyPressMsg{key(tea.KeyEscape)}},
		// The first Ctrl-C, Esc's sibling arm (design §6 rule 7): its own hint
		// drawn in the same place, over a turn it has already cancelled — proof
		// the two arms use the same line without merging into one state.
		{name: "ctrlc-armed", prompt: prompt, keys: []tea.KeyPressMsg{ctrlCKey}},
		// A step that has thought and said nothing yet, and the same step once the
		// reply has started under it. Two states rather than one because they are
		// the only frames where the faint segment is the whole of what a reader has
		// to go on, and because a message with no text in it drew nothing at all
		// until thinking was rendered.
		{name: "thinking", prompt: prompt, events: []agent.Event{
			{Kind: agent.EventAssistantDelta, Message: thinking},
		}},
		{name: "streaming", prompt: prompt, events: []agent.Event{
			{Kind: agent.EventAssistantDelta, Message: fragment},
		}},
		{name: "tools", prompt: prompt, events: tools},
		// The same conversation with every card opened, which is the only state
		// that draws what a tool actually returned.
		{name: "expanded", prompt: prompt, events: tools, keys: []tea.KeyPressMsg{expandKey}},
		// A gated call, waiting on the user. The cards for the batch are already
		// drawn and queued, and the question stands under them where it was
		// asked — which is the whole of what "inline" means here.
		{name: "prompt", prompt: prompt, events: []agent.Event{
			{Kind: agent.EventAssistantDelta, Message: explained},
			{Kind: agent.EventAssistantEnd, Message: explained},
		}, ask: &permission.Request{
			CallID: "call_2",
			Tool:   "edit",
			Action: permission.ActionEdit,
			Path:   "auth.go",
		}},
		// A batch part way through: the second call is running and the first has
		// not started, so the frame is the one that would reorder itself if the
		// cards were built from tool_start rather than from the message above.
		{name: "working", prompt: prompt, events: []agent.Event{
			{Kind: agent.EventAssistantDelta, Message: explained},
			{Kind: agent.EventAssistantEnd, Message: explained},
			{Kind: agent.EventToolStart, CallID: "call_2", Tool: "edit"},
		}},
		// Two file changes, drawn. Nothing here presses a key: a card holding a
		// diff opens itself, which is the difference between a transcript that
		// shows what changed and one that shows a path.
		{name: "diff", prompt: prompt, events: []agent.Event{
			{Kind: agent.EventAssistantDelta, Message: changed},
			{Kind: agent.EventAssistantEnd, Message: changed},
			{Kind: agent.EventToolStart, CallID: "call_3", Tool: "edit"},
			{Kind: agent.EventToolEnd, CallID: "call_3", Tool: "edit", Result: edited},
			{Kind: agent.EventToolStart, CallID: "call_4", Tool: "write"},
			{Kind: agent.EventToolEnd, CallID: "call_4", Tool: "write", Result: created},
			{Kind: agent.EventTurnEnd},
		}},
		// A turn caught two and a half seconds in, which is the one frame here
		// that has an animation on it: the spinner is on the glyph that elapsed
		// names, the activity line carries the turn's own running time, and the
		// status line is reading what the provider has reported of this step so
		// far rather than the total it will settle on.
		{name: "animated", prompt: prompt, after: 2500 * time.Millisecond, events: []agent.Event{
			{Kind: agent.EventAssistantDelta, Message: spent(
				reply("Reading `auth_test.go` now. The header is parsed"),
				llm.Usage{Input: 812, Output: 47, CacheRead: 11204})},
		}},
		// A step that failed mid-reply: the fragment is settled rather than left
		// open, and the failure is drawn under it.
		{name: "error", prompt: prompt, events: []agent.Event{
			{Kind: agent.EventAssistantDelta, Message: fragment},
			{Kind: agent.EventError, Err: errors.New("the provider closed the stream mid-message")},
			{Kind: agent.EventTurnEnd},
		}},
	}
}

// typedLine is a line typed into the input and sent.
func typedLine(line string) []tea.KeyPressMsg {
	keys := make([]tea.KeyPressMsg, 0, len(line)+1)
	for _, r := range line {
		keys = append(keys, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return append(keys, key(tea.KeyEnter))
}

// asking is a reply that also asked for tools, the blocks in the order the
// model wrote them.
func asking(msg *llm.Message, calls ...llm.Block) *llm.Message {
	msg.Content = append(msg.Content, calls...)
	return msg
}

// spent is a reply the provider reported counts for, which is what puts numbers
// on the status line rather than the zeros a session before its first turn has.
func spent(msg *llm.Message, u llm.Usage) *llm.Message {
	msg.Usage = u
	return msg
}

// diff joins the lines of a unified diff the way go-udiff hands one over:
// newline-separated, header pair included, and ending in a newline.
func diff(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

// TestViewGoldens freezes the frame each state draws, so a change to any of the
// styling, the wrapping or the order things appear in shows up as a diff rather
// than as nothing at all.
//
// Regenerate with `go test ./internal/tui -update` — the flag belongs to the
// golden package, which is why nothing here declares it. Regenerate the whole
// test rather than one subtest: the markdown renderer memoises the head of a
// reply across frames (internals §4.4), so a run that skips its neighbours does
// not necessarily reach a golden by the same path the suite does.
func TestViewGoldens(t *testing.T) {
	// The one thing about a frame that is not the UI's to decide. Glamour measures
	// a reply in terminal cells, and charmbracelet/x/ansi widens every East Asian
	// ambiguous character to two of them when RUNEWIDTH_EASTASIAN is set — the
	// bullet glamour draws a list with among them — reading the variable once at
	// init with no override. Named here because the diff alone reads as a styling
	// regression nobody made.
	if wide, err := strconv.ParseBool(os.Getenv("RUNEWIDTH_EASTASIAN")); err == nil && wide {
		t.Fatal("RUNEWIDTH_EASTASIAN is set and these frames were recorded without it; unset it to " +
			"compare them, or every diff below is the width of a bullet rather than a change to the UI")
	}

	states := snapshots()
	if len(states) == 0 {
		t.Fatal("there are no states to record, and a golden suite that snapshots nothing passes forever")
	}

	drawn := make(map[string]string, len(states))
	for _, state := range states {
		t.Run(state.name, func(t *testing.T) {
			frame := draw(t, state)
			if strings.TrimSpace(frame) == "" {
				t.Fatal("the state drew a blank frame, which every other state would match too")
			}
			drawn[state.name] = frame
			golden.RequireEqual(t, frame)
		})
	}

	distinct(t, drawn)
	recorded(t, states)
}

// TestBannerGolden freezes the identity block Run appends ahead of the
// conversation (tui.go), on its own rather than folded into snapshots(): every
// state there shares one goldenConfig, so the block would be the same eleven
// lines at the top of all fourteen goldens — adding nothing to any of them but
// the cost of re-reviewing every one on a change to wording nothing else there
// is about.
func TestBannerGolden(t *testing.T) {
	turner := newTurner(agent.ErrInterrupted)
	m := newModel(t.Context(), turner, goldenConfig())
	m.chat.Append(banner(goldenConfig()))
	m.permissions = &answers{decided: true}
	m.now = func() time.Time { return goldenNow }
	m.status.branch = goldenBranch

	tm := teatest.NewTestModel(t, m,
		teatest.WithInitialTermSize(goldenWidth, goldenHeight),
		teatest.WithProgramOptions(tea.WithoutRenderer()),
	)

	frame := quit(t, tm).View().Content
	if strings.TrimSpace(frame) == "" {
		t.Fatal("the banner state drew a blank frame")
	}
	golden.RequireEqual(t, frame)
}

// TestAResizeRedrawsTheConversationAtItsNewWidth is what the goldens cannot say
// on their own: a snapshot records whatever the harness produced, so something
// asserted rather than recorded has to prove the program is really running the
// model — here a terminal that changed size after the conversation had lines in
// it, which is the resize a golden taken at a fixed width never sees.
func TestAResizeRedrawsTheConversationAtItsNewWidth(t *testing.T) {
	const (
		prompt = "fix the failing auth test and say which middleware parsed the header twice"
		narrow = 24
	)

	tm, turner, _ := program(t)
	submit(t, tm, turner, prompt)
	tm.Send(tea.WindowSizeMsg{Width: narrow, Height: goldenHeight})

	frame := quit(t, tm).View().Content
	if !strings.Contains(words(frame), prompt) {
		t.Fatalf("the prompt never reached the conversation:\n%s", frame)
	}
	// Measured in cells rather than runes: the chrome is styled, and counting the
	// escape sequences would call a six-column status line twenty wide — a
	// failure about nothing, on every line the UI colours.
	for _, line := range strings.Split(frame, "\n") {
		if n := ansi.StringWidth(line); n > narrow {
			t.Errorf("a line runs %d columns into a terminal %d wide, so the resize was never drawn: %q",
				n, narrow, line)
		}
	}
}

// draw runs one state through a real Bubble Tea program and returns the frame it
// ended on.
func draw(t *testing.T, state snapshot) string {
	t.Helper()

	tm, turner, clock := program(t)
	if state.prompt != "" {
		submit(t, tm, turner, state.prompt)
	}
	for _, ev := range state.events {
		tm.Send(agentMsg{event: ev})
	}
	if state.after > 0 {
		clock.pass(state.after)
		// And a beat behind it, because a running card is handed its elapsed time
		// by the beat rather than reading the clock itself (chat.Call.Elapsed).
		tm.Send(tickMsg{})
	}
	if state.ask != nil {
		tm.Send(promptMsg{request: *state.ask})
	}
	for _, key := range state.keys {
		tm.Send(key)
	}

	return quit(t, tm).View().Content
}

// program starts the root model under teatest, with no terminal at either end.
// The renderer is off because nothing here reads the output stream, and View is
// called after every Update all the same.
//
// Deliberately bannerless: Run appends that item (tui.go), newModel does not,
// and every state drawn through this helper wants the conversation alone —
// TestBannerGolden is the one frame that adds it back.
//
// The turner it comes back with stays in Send until the test's context ends,
// which is what holds a turn open long enough for a frame to be taken mid-flight.
func program(t *testing.T) (*teatest.TestModel, *turner, *clock) {
	t.Helper()

	turner := newTurner(agent.ErrInterrupted)
	m := newModel(t.Context(), turner, goldenConfig())
	// Enough of a permission service for a question to be drawn as one. Without
	// it a question is drawn as the notice saying nothing can answer it, which is
	// a different state and not the one these frames are recording.
	m.permissions = &answers{decided: true}
	// A clock the snapshot moves rather than the machine. Real time would put a
	// duration into the frames that depends on how fast the queue drained, and a
	// beat firing between two sends would move it again; a state that wants an
	// animation on it names the instant instead (snapshot.after).
	fake := newClock(goldenNow)
	m.now = fake.read
	m.status.branch = goldenBranch

	return teatest.NewTestModel(t, m,
		teatest.WithInitialTermSize(goldenWidth, goldenHeight),
		teatest.WithProgramOptions(tea.WithoutRenderer()),
	), turner, fake
}

// clock is time a test moves by hand. Atomic because a snapshot moves it from
// the test's own goroutine while a running program reads it on Bubble Tea's;
// the tests that drive Update themselves never cross that line and pay nothing
// for it.
type clock struct{ nanos atomic.Int64 }

func newClock(at time.Time) *clock {
	c := &clock{}
	c.nanos.Store(at.UnixNano())
	return c
}

func (c *clock) read() time.Time      { return time.Unix(0, c.nanos.Load()) }
func (c *clock) pass(d time.Duration) { c.nanos.Add(int64(d)) }

// submit types a line, sends it, and waits for the turn it starts. The turn runs
// on a goroutine of its own, so waiting is what orders everything after it
// rather than against it: the turn's own turnDone landing in the middle leaves
// the frame busy or idle depending on the scheduler.
func submit(t *testing.T, tm *teatest.TestModel, turn *turner, text string) {
	t.Helper()

	tm.Type(text)
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	started(t, turn.started)
}

// quit stops the program and hands back the model it ended on. Quit travels the
// same channel as everything sent before it, so the program stops having handled
// all of it rather than at whatever point it had reached.
func quit(t *testing.T, tm *teatest.TestModel) Model {
	t.Helper()

	if err := tm.Quit(); err != nil {
		t.Fatalf("quitting the program: %v", err)
	}
	last := tm.FinalModel(t, teatest.WithFinalTimeout(settle))
	final, ok := last.(Model)
	if !ok {
		t.Fatalf("the program returned a %T rather than the root model", last)
	}
	return final
}

// distinct fails when two states drew the same frame, and is what stands
// between this suite and the quietest failure a golden test has. A harness that
// stopped delivering anything to the model still records five frames, all of
// them the empty one — and -update then makes that the expectation, for good.
func distinct(t *testing.T, drawn map[string]string) {
	t.Helper()

	by := make(map[string]string, len(drawn))
	for _, name := range slices.Sorted(maps.Keys(drawn)) {
		if first, ok := by[drawn[name]]; ok {
			t.Errorf("%q drew the same frame as %q, so one of the two is not the state it is named for", name, first)
			continue
		}
		by[drawn[name]] = name
	}
}

// recorded checks what is on disk against the states above. Either half alone is
// quiet about the failure that matters: a golden no state names is compared to
// nothing for the rest of its life, and a state with no golden is a frame nobody
// has ever looked at — which -update would then write and call a pass.
func recorded(t *testing.T, states []snapshot) {
	t.Helper()

	files, err := filepath.Glob(filepath.Join("testdata", t.Name(), "*.golden"))
	if err != nil {
		t.Fatalf("looking for the recorded frames: %v", err)
	}

	have := make([]string, 0, len(files))
	for _, f := range files {
		have = append(have, strings.TrimSuffix(filepath.Base(f), ".golden"))
	}
	want := make([]string, 0, len(states))
	for _, state := range states {
		want = append(want, state.name)
	}
	slices.Sort(have)
	slices.Sort(want)

	if !slices.Equal(have, want) {
		t.Errorf("testdata holds %v and the states are %v; regenerate every golden with -update, "+
			"and delete by hand the ones no state names any more", have, want)
	}
}
