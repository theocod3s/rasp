package tui

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/fake"
	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tui/chat"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// TestTheModeIsOnEveryFrame. Which mode a session is in is the question the
// permissive ones make expensive to get wrong (design §7.8), so it is on the
// screen in states where there is nothing else to draw at all and in states
// where something has gone wrong — the two a line built only for the ordinary
// case tends to miss.
func TestTheModeIsOnEveryFrame(t *testing.T) {
	asked := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockText, Text: "Reading it."},
		{Type: llm.BlockToolUse, ID: "call_1", Name: "read"},
	}}

	for _, tc := range []struct {
		name   string
		width  int
		events []agent.Event
	}{
		{name: "nothing has happened yet", width: goldenWidth},
		{name: "before the terminal has reported a size"},
		{name: "mid-stream", width: goldenWidth, events: []agent.Event{
			{Kind: agent.EventAssistantDelta, Message: asked},
		}},
		{name: "a tool is running", width: goldenWidth, events: []agent.Event{
			{Kind: agent.EventAssistantEnd, Message: asked},
			{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"},
		}},
		{name: "the turn failed", width: goldenWidth, events: []agent.Event{
			{Kind: agent.EventError, Err: errors.New("the provider closed the stream mid-message")},
			{Kind: agent.EventTurnEnd},
		}},
		{name: "the terminal is too narrow for anything else", width: 12, events: []agent.Event{
			{Kind: agent.EventTurnEnd, Usage: llm.Usage{Input: 900, Output: 120}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m tea.Model = newModel(t.Context(), newTurner(nil), Config{
				Model: "anthropic/claude-opus-5",
				Mode:  permission.ModePlan,
			})
			m, _ = m.Update(tea.WindowSizeMsg{Width: tc.width, Height: goldenHeight})
			for _, ev := range tc.events {
				m, _ = m.Update(agentMsg{event: ev})
			}

			if frame := words(m.View().Content); !strings.Contains(frame, string(permission.ModePlan)) {
				t.Errorf("the frame never names the mode:\n%s", frame)
			}
		})
	}
}

// TestAModelNobodyNamedStillDrawsAMode. Every construction path has to satisfy
// the rule above, and the zero Model is the one that would not: it is what the
// tests around it build, and it is what a caller who passed no Config gets.
func TestAModelNobodyNamedStillDrawsAMode(t *testing.T) {
	frame := words(Model{}.View().Content)
	if !strings.Contains(frame, string(permission.ModeManual)) {
		t.Errorf("a model configured with nothing draws no mode, and manual is the default it "+
			"would be running under:\n%s", frame)
	}
}

// TestTheLineCountsWhatTheTurnSpent walks a two-step turn and reads the numbers
// off the frame. The counts are the provider's, summed here the way the loop
// sums them for itself.
func TestTheLineCountsWhatTheTurnSpent(t *testing.T) {
	first := reply("Reading it.")
	first.Usage = llm.Usage{Input: 400, Output: 60, CacheRead: 11000, CacheWrite: 200}
	second := reply("The header is parsed twice.")
	second.Usage = llm.Usage{Input: 700, Output: 90, CacheRead: 11200}

	var m tea.Model = newModel(t.Context(), newTurner(nil), Config{Mode: permission.ModeManual})
	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventAssistantEnd, Message: first}})

	// The first call's own counts, prompt and reply together — cache hits and
	// writes are prompt, and Input excludes both.
	if frame, want := words(m.View().Content), "ctx 11.7k"; !strings.Contains(frame, want) {
		t.Errorf("the frame does not read %q:\n%s", want, frame)
	}

	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventAssistantEnd, Message: second}})
	frame := words(m.View().Content)
	for _, want := range []string{"ctx 12k", "in 23.5k", "out 150"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the frame does not read %q:\n%s", want, frame)
		}
	}
}

// TestATurnsTotalReplacesTheRunningSumRatherThanAddingToIt. The loop sums a
// turn's usage over the same messages the UI has already counted one at a time
// (agent/step.go), so a session total that added the turn's own report to its
// running sum would say double after the first turn and stay wrong for the rest
// of the session.
func TestATurnsTotalReplacesTheRunningSumRatherThanAddingToIt(t *testing.T) {
	step := reply("Done.")
	step.Usage = llm.Usage{Input: 1200, Output: 300, CacheRead: 4000}
	total := llm.Usage{Input: 1200, Output: 300, CacheRead: 4000}

	var m tea.Model = newModel(t.Context(), newTurner(nil), Config{Mode: permission.ModeManual})
	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventAssistantEnd, Message: step}})

	before := words(m.View().Content)
	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventTurnEnd, Usage: total}})

	if after := words(m.View().Content); after != before {
		t.Errorf("the turn ending changed the counts:\nbefore %s\nafter  %s", before, after)
	}
	for _, want := range []string{"in 5.2k", "out 300"} {
		if !strings.Contains(before, want) {
			t.Fatalf("the frame does not read %q, so the comparison above holds over the wrong "+
				"numbers:\n%s", want, before)
		}
	}
}

// TestOutClimbsWhileAReplyStreamsAndLandsOnTheStepsTotal drives a real turn out
// of a provider that reports its counts as they accrue, which is the only way to
// see what actually rides a delta: nothing is sent alongside the accumulated
// message, so what the line can show mid-stream is whatever usage that message
// is carrying at the moment the delta arrives.
//
// The script is shaped like a wire that reports the prompt before the first
// token and the reply in instalments after it. The counts are cumulative for the
// step, so a line that added them instead of replacing would end the turn saying
// 120 where the loop says 60.
func TestOutClimbsWhileAReplyStreamsAndLandsOnTheStepsTotal(t *testing.T) {
	const prompt, cached = 900, 4200
	total := llm.Usage{Input: prompt, Output: 60, CacheRead: cached}

	// The instalments deliberately do not sum to the total. They are cumulative
	// reports of one step, so a line that added them would end somewhere past it
	// — and with figures that happened to add up, that bug draws the right number
	// by arithmetic accident.
	provider := fake.New(
		fake.Usage(llm.Usage{Input: prompt, CacheRead: cached}),
		fake.Text("The header "),
		fake.Usage(llm.Usage{Input: prompt, Output: 21, CacheRead: cached}),
		fake.Text("is parsed "),
		fake.Usage(llm.Usage{Input: prompt, Output: 45, CacheRead: cached}),
		fake.Text("twice."),
		fake.Usage(total),
		fake.Done(llm.StopEndTurn),
	)

	// The filter runs on the rendering goroutine, ahead of Update, and reads the
	// line each event is about to change — so this is the sequence of counts the
	// turn actually put on the screen. Everything it writes is read after the
	// program has returned, which is what orders the two; nothing here fails the
	// test from that goroutine, where Fatal would stop the wrong one.
	var (
		ended = make(chan struct{})
		over  bool
		drawn []int
	)
	watch := func(model tea.Model, msg tea.Msg) tea.Msg {
		ev, isEvent := msg.(agentMsg)
		if root, ok := model.(Model); ok && isEvent && !over {
			n, found := outCount(root.status.Render(0, styles.Dark))
			if !found {
				n = -1
			}
			drawn = append(drawn, n)
		}
		if isEvent && ev.event.Kind == agent.EventTurnEnd {
			over = true
			close(ended)
		}
		return msg
	}

	b := newBridge()
	prog := tea.NewProgram(Model{}, append(headless(), tea.WithFilter(watch))...)
	b.start(prog)

	finished := make(chan tea.Model, 1)
	go func() {
		model, _ := prog.Run()
		finished <- model
	}()

	ag, err := agent.New(agent.Config{
		Provider:  provider,
		Tools:     tool.NewRegistry(nil),
		Model:     "test-model",
		MaxTokens: 1024,
		Events:    b.handle,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := ag.Send(t.Context(), "why is the header parsed twice"); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}

	select {
	case <-ended:
	case <-time.After(settle):
		t.Fatal("the turn ended and the UI never heard about it")
	}
	prog.Quit()

	var final Model
	select {
	case model := <-finished:
		root, ok := model.(Model)
		if !ok {
			t.Fatalf("the program returned a %T rather than the root model", model)
		}
		final = root
	case <-time.After(settle):
		t.Fatal("the program did not return after Quit")
	}
	b.stop()

	if slices.Contains(drawn, -1) {
		t.Fatalf("some frame drew a status line with no out segment on it, so the counts below are "+
			"about a line that had moved: %v", drawn)
	}
	if len(drawn) < 2 {
		t.Fatalf("the turn drew %d line(s), so there is no sequence here to check: %v", len(drawn), drawn)
	}

	// Climbed, and never fell: a count going down would mean an instalment was
	// taken for the whole report.
	var climbed bool
	for i := 1; i < len(drawn); i++ {
		switch {
		case drawn[i] < drawn[i-1]:
			t.Fatalf("the count fell from %d to %d part way through the turn: %v", drawn[i-1], drawn[i], drawn)
		case drawn[i] > drawn[i-1]:
			climbed = true
		}
	}
	if !climbed {
		t.Errorf("the count never moved off %v, so nothing was drawn until the step ended", drawn)
	}
	// And climbed mid-stream, not merely at the end: a line reading only the
	// finished message would satisfy everything above.
	if mid := slices.Max(drawn); mid == 0 || mid > total.Output {
		t.Errorf("the largest count drawn while the turn ran is %d, and the step's own total is %d",
			mid, total.Output)
	}

	// Every counter, not out alone: the line lands on exactly what a session that
	// had spent this turn and nothing else would read, which is the assertion an
	// instalment counted twice fails wherever it landed.
	settled := ansi.Strip(final.status.Render(0, styles.Dark))
	want := ansi.Strip(status{spent: total, context: sent(total) + total.Output}.Render(0, styles.Dark))
	if settled != want {
		t.Errorf("the turn ended reading\n%q\nand the loop's own total is\n%q", settled, want)
	}
	if !strings.Contains(settled, "out "+strconv.Itoa(total.Output)) {
		t.Fatalf("neither line names the reply count, so the comparison above is over two lines that "+
			"agree about nothing: %q", settled)
	}
}

// TestAProviderThatReportsNothingUntilTheEndInventsNothing. An endpoint that
// sends its counts in one final chunk hands the UI a message whose usage is zero
// for the whole stream — and a line that wrote that down would blank counts it
// was reading correctly a moment before, on every stream, for the length of it.
func TestAProviderThatReportsNothingUntilTheEndInventsNothing(t *testing.T) {
	first := reply("Reading it.")
	first.Usage = llm.Usage{Input: 400, Output: 60, CacheRead: 11000}

	var m tea.Model = newModel(t.Context(), newTurner(nil), Config{Mode: permission.ModeManual})
	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventAssistantEnd, Message: first}})
	settled := words(m.View().Content)

	for _, want := range []string{"ctx 11.5k", "in 11.4k", "out 60"} {
		if !strings.Contains(settled, want) {
			t.Fatalf("the frame does not read %q, so the comparison below is over the wrong numbers:\n%s",
				want, settled)
		}
	}

	// The next step's deltas, carrying nothing at all.
	m, _ = m.Update(agentMsg{event: agent.Event{
		Kind: agent.EventAssistantDelta, Message: reply("The header is parsed"),
	}})

	for _, want := range []string{"ctx 11.5k", "in 11.4k", "out 60"} {
		if frame := words(m.View().Content); !strings.Contains(frame, want) {
			t.Errorf("the frame no longer reads %q; a report of nothing was taken at face value:\n%s",
				want, frame)
		}
	}
}

// outCount reads the reply count off the line, and says whether it found one.
// Zero is a real count, so a reader that answered it for a segment renamed or
// dropped would satisfy every comparison above by never finding anything.
func outCount(line string) (int, bool) {
	fields := strings.Fields(ansi.Strip(line))
	for i, field := range fields {
		if field != "out" || i+1 >= len(fields) {
			continue
		}
		n, err := strconv.Atoi(fields[i+1])
		return n, err == nil
	}
	return 0, false
}

// TestAStatusUpdateRedrawsNoPartOfTheConversation is the freeze from the other
// side (internals §4.5): the status line is rendered beside the conversation
// rather than as an item in it, so a frame in which only these numbers moved
// costs no item render at all. The count is the assertion, and the frames
// having changed is what stops it from passing on a line that says nothing.
func TestAStatusUpdateRedrawsNoPartOfTheConversation(t *testing.T) {
	var runs int
	m := newModel(t.Context(), newTurner(nil), Config{
		Model: "anthropic/claude-opus-5",
		Mode:  permission.ModeManual,
	})
	m.width = goldenWidth
	m.chat.Set("reply", counted{
		Message: chat.Message{Content: *reply("The header is parsed twice."), Done: true},
		runs:    &runs,
	})

	frames := []string{m.View().Content}
	if runs != 1 {
		t.Fatalf("the first frame drew the conversation %d time(s); the count below rests on the "+
			"item having been drawn exactly once already", runs)
	}
	warm := runs

	var tm tea.Model = m
	for _, u := range []llm.Usage{
		{Input: 900, Output: 120, CacheRead: 8000},
		{Input: 1400, Output: 260, CacheRead: 8000},
	} {
		tm, _ = tm.Update(agentMsg{event: agent.Event{Kind: agent.EventAssistantEnd, Message: usage(u)}})
		tm, _ = tm.Update(agentMsg{event: agent.Event{Kind: agent.EventTurnEnd, Usage: u}})
		frames = append(frames, tm.View().Content)
	}

	for i := 1; i < len(frames); i++ {
		if frames[i] == frames[i-1] {
			t.Fatalf("frame %d is the one before it, so nothing about the status line moved and the "+
				"count below is about a UI that drew nothing:\n%s", i, frames[i])
		}
	}
	if redrawn := runs - warm; redrawn != 0 {
		t.Errorf("%d conversation item render(s) across %d frames in which only the status line "+
			"changed", redrawn, len(frames)-1)
	}
}

// TestSegmentsDropFromTheRightAndTheModeStays. A narrow terminal is where the
// status line matters most and has least room, so what goes is decided rather
// than left to the wrap: whole segments from the right, the mode last of all.
func TestSegmentsDropFromTheRightAndTheModeStays(t *testing.T) {
	s := status{
		model:   "anthropic/claude-opus-5",
		mode:    permission.ModeManual,
		context: 12_800,
		spent:   llm.Usage{Input: 2342, Output: 205, CacheRead: 22408},
	}

	for _, tc := range []struct {
		width int
		want  string
	}{
		{width: 0, want: "manual · anthropic/claude-opus-5 · ctx 12.8k · in 24.8k · out 205 · cost —"},
		{width: 80, want: "manual · anthropic/claude-opus-5 · ctx 12.8k · in 24.8k · out 205 · cost —"},
		{width: 70, want: "manual · anthropic/claude-opus-5 · ctx 12.8k · in 24.8k · out 205"},
		{width: 60, want: "manual · anthropic/claude-opus-5 · ctx 12.8k · in 24.8k"},
		{width: 40, want: "manual · anthropic/claude-opus-5"},
		{width: 20, want: "manual"},
		{width: 4, want: "manu"},
	} {
		t.Run(strconv.Itoa(tc.width)+" columns", func(t *testing.T) {
			line := ansi.Strip(s.Render(tc.width, styles.Dark))
			if line != tc.want {
				t.Errorf("at %d columns the line reads\n%q\nwant\n%q", tc.width, line, tc.want)
			}
			if tc.width > 0 && ansi.StringWidth(line) > tc.width {
				t.Errorf("the line is %d columns wide in a terminal %d wide", ansi.StringWidth(line), tc.width)
			}
		})
	}
}

// TestTheModesWithSomethingToSayAreColoured. Plan and auto change what a tool
// may do without being asked, and each has a colour of its own; manual is the
// default and draws in the terminal's foreground (design §7.8).
func TestTheModesWithSomethingToSayAreColoured(t *testing.T) {
	drawn := func(mode permission.Mode) string {
		return status{mode: mode}.Render(0, styles.Dark)
	}

	if line := drawn(permission.ModeManual); !strings.HasPrefix(line, string(permission.ModeManual)) {
		t.Errorf("manual opens with an escape sequence, so it is not the terminal's own colour: %q", line)
	}

	// The escape a mode's own name is drawn behind, which is what a colour is on
	// a line whose remaining segments are coloured too.
	tint := func(mode permission.Mode) string {
		line := drawn(mode)
		return line[:strings.Index(line, string(mode))]
	}
	for _, mode := range []permission.Mode{permission.ModePlan, permission.ModeAuto} {
		if tint(mode) == "" {
			t.Errorf("%s is drawn in no colour at all: %q", mode, drawn(mode))
		}
	}
	if tint(permission.ModePlan) == tint(permission.ModeAuto) {
		t.Error("plan and auto are drawn in the same colour, so the line says which mode only in words")
	}

	// A mode this build has no token for draws plainly rather than borrowing one
	// of the two above, which would say something about it that nothing knows.
	if line := drawn("yolo"); !strings.HasPrefix(line, "yolo") {
		t.Errorf("an unknown mode took a colour: %q", line)
	}
}

// TestCostIsAnAbsenceRatherThanAGuess. A price is per model and comes from the
// catalog (design §10.2), which nothing fetches yet. The dash is deliberate: a
// hardcoded table would put a wrong number where the reader expects the bill,
// and this test is what a change replacing it has to argue with.
func TestCostIsAnAbsenceRatherThanAGuess(t *testing.T) {
	line := ansi.Strip(status{spent: llm.Usage{Input: 9000, Output: 4000}}.Render(0, styles.Dark))
	if !strings.HasSuffix(line, "cost —") {
		t.Errorf("the line does not end in an unknown cost: %q", line)
	}
}

func TestTokenCountsAreExactWhereItMattersAndRoundedWhereItDoesNot(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{n: 0, want: "0"},
		{n: 999, want: "999"},
		{n: 1000, want: "1k"},
		{n: 1049, want: "1k"},
		{n: 1234, want: "1.2k"},
		{n: 12_800, want: "12.8k"},
		{n: 999_949, want: "999.9k"},
		{n: 999_950, want: "1M"},
		{n: 1_000_000, want: "1M"},
		{n: 2_470_000, want: "2.5M"},
	} {
		if got := tokens(tc.n); got != tc.want {
			t.Errorf("tokens(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// usage is a finished reply carrying nothing but the counts the provider
// reported for it.
func usage(u llm.Usage) *llm.Message {
	return &llm.Message{Role: llm.RoleAssistant, Usage: u}
}

// counted renders exactly as a conversation item does and records how often it
// was asked to. A frame that reused a frozen string and one that rebuilt it are
// otherwise the same bytes, which is the whole thing under test.
type counted struct {
	chat.Message
	runs *int
}

func (c counted) Render(width int) string {
	*c.runs++
	return c.Message.Render(width)
}
