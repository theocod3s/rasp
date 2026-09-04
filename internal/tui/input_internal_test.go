package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tui/chat"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// TestTheInputSitsInsideItsOwnFrame. The line the user types on is the one part
// of the screen that is theirs, and the rules are what separate it from a
// conversation that has been scrolling past it.
func TestTheInputSitsInsideItsOwnFrame(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	m.width = goldenWidth

	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	i := caretLine(t, lines)

	if i == 0 || i == len(lines)-1 {
		t.Fatalf("the input is line %d of %d, so it has no room for a frame:\n%s", i, len(lines),
			strings.Join(lines, "\n"))
	}
	for _, edge := range []int{i - 1, i + 1} {
		rule := strings.Repeat(frameRule, goldenWidth)
		if lines[edge] != rule {
			t.Errorf("line %d reads %q, want the frame's rule", edge, lines[edge])
		}
	}
	// And the footer under it rather than above: the frame is the bottom of the
	// conversation, and what the session *is* goes below the whole of it.
	if last := lines[len(lines)-1]; !strings.Contains(last, string(permission.ModeManual)) {
		t.Errorf("the last line of the frame is %q, and the footer is what ends it", last)
	}
}

// TestATerminalOfUnknownWidthDrawsNoRule. A rule is drawn across the terminal,
// and a terminal that has not said how wide it is has nothing to draw across —
// a fixed guess would be a line of the wrong length on the first frame of every
// session.
func TestATerminalOfUnknownWidthDrawsNoRule(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})

	frame := m.View().Content
	if strings.Contains(frame, frameRule) {
		t.Errorf("a frame drawn before the terminal reported a size carries a rule:\n%s", frame)
	}
	if !strings.Contains(frame, chat.Caret) {
		t.Errorf("the input line is gone with the frame around it:\n%s", frame)
	}
}

// TestTheEmptyLineAsks. An empty input line says nothing about what to do with
// it, and this is the first thing a new session shows anyone.
func TestTheEmptyLineAsks(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	m.width = goldenWidth

	line := typedLineOf(t, m)
	if !strings.Contains(line, placeholder) {
		t.Errorf("the empty input line reads %q, and the invitation is not on it", line)
	}
	// Faint, not plain: it is a prompt to the user rather than something they
	// wrote, and a placeholder at full contrast reads as text already typed.
	if !strings.Contains(m.typing(), styleOf(m, placeholder)) {
		t.Errorf("the placeholder is drawn at full contrast: %q", m.typing())
	}

	m = typed(m, "fix the auth test")

	line = typedLineOf(t, m)
	if strings.Contains(line, placeholder) {
		t.Errorf("the invitation is still on a line that has been typed on: %q", line)
	}
	if !strings.Contains(line, "fix the auth test") {
		t.Errorf("the input line reads %q, and what was typed is not on it", line)
	}
}

// TestTheFrameHoldsWhileAQuestionStands. A permission question takes the
// keyboard off the input line (prompt.go) — it must not take the line itself,
// or answering one would move everything on the screen.
func TestTheFrameHoldsWhileAQuestionStands(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	m.permissions = &answers{decided: true}
	m.width = goldenWidth

	before := strings.Count(ansi.Strip(m.View().Content), strings.Repeat(frameRule, goldenWidth))
	m, _ = m.ask(permission.Request{CallID: "call_1", Tool: "edit", Action: permission.ActionEdit, Path: "auth.go"})

	frame := ansi.Strip(m.View().Content)
	if !strings.Contains(frame, "needs your approval") {
		t.Fatalf("the question was never drawn, so nothing here is about a frame under one:\n%s", frame)
	}
	if after := strings.Count(frame, strings.Repeat(frameRule, goldenWidth)); after != before {
		t.Errorf("the frame has %d rule(s) with a question open and %d without one:\n%s", after, before, frame)
	}
	if line := typedLineOf(t, m); !strings.Contains(line, placeholder) {
		t.Errorf("the input line reads %q while a question stands", line)
	}
}

// TestTypingRedrawsNoPartOfTheConversation is the no-re-render guarantee over
// the bottom chrome (internals §4.5): the frame, the line inside it and the
// footer are drawn from the model's own fields, so a keystroke costs no item
// render at all — which is what keeps a long transcript from being rebuilt on
// every letter of a prompt.
func TestTypingRedrawsNoPartOfTheConversation(t *testing.T) {
	var runs int
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
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

	for _, text := range []string{"fix", " the", " auth test"} {
		m = typed(m, text)
		frames = append(frames, m.View().Content)
	}

	for i := 1; i < len(frames); i++ {
		if frames[i] == frames[i-1] {
			t.Fatalf("frame %d is the one before it, so nothing about the input moved and the count "+
				"below is about a UI that drew nothing:\n%s", i, frames[i])
		}
	}
	if redrawn := runs - warm; redrawn != 0 {
		t.Errorf("%d conversation item render(s) across %d frames in which only what was typed changed",
			redrawn, len(frames)-1)
	}
}

// TestTheInvitationIsCutToTheTerminal. It is the UI's own words rather than the
// user's, so a terminal too narrow for it takes as much as fits: wrapped, it
// would push the frame's lower rule down a line and take the footer off a short
// screen with it.
func TestTheInvitationIsCutToTheTerminal(t *testing.T) {
	const narrow = 8

	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	m = update(m, tea.WindowSizeMsg{Width: narrow, Height: goldenHeight})

	for _, line := range strings.Split(m.View().Content, "\n") {
		if n := ansi.StringWidth(line); n > narrow {
			t.Errorf("a line runs %d columns into a terminal %d wide: %q", n, narrow, line)
		}
	}
}

// TestTheBottomChromeIsOnEveryFrame walks the states a turn passes through and
// checks the frame is still under all of them. The states are the ones that
// draw something of their own between the conversation and the input — an
// activity line, an error — where a chrome appended in the wrong order would
// leave the input somewhere other than the bottom.
func TestTheBottomChromeIsOnEveryFrame(t *testing.T) {
	asked := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockText, Text: "Reading it."},
		{Type: llm.BlockToolUse, ID: "call_1", Name: "read"},
	}}

	for _, tc := range []struct {
		name   string
		events []agent.Event
	}{
		{name: "nothing has happened yet"},
		{name: "a tool is running", events: []agent.Event{
			{Kind: agent.EventAssistantEnd, Message: asked},
			{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"},
		}},
		{name: "the turn failed", events: []agent.Event{
			{Kind: agent.EventError, Err: errors.New("the provider closed the stream mid-message")},
			{Kind: agent.EventTurnEnd},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
			m = update(m, tea.WindowSizeMsg{Width: goldenWidth, Height: goldenHeight})
			for _, ev := range tc.events {
				m = update(m, agentMsg{event: ev})
			}

			lines := strings.Split(ansi.Strip(m.View().Content), "\n")
			if i := caretLine(t, lines); i != len(lines)-3 {
				t.Errorf("the input is line %d of %d, and the rule and the footer are what follow it:\n%s",
					i, len(lines), strings.Join(lines, "\n"))
			}
		})
	}
}

// TestEnterSendsTheWholeDraftAndTheOtherKeysGrowIt. Three keys break a line
// because terminals differ in which of them they deliver (model.go breaksLine),
// and Enter sends what they built as one message rather than one per line.
func TestEnterSendsTheWholeDraftAndTheOtherKeysGrowIt(t *testing.T) {
	const want = "first line\nsecond line"

	for _, tc := range []struct {
		name  string
		press tea.KeyPressMsg
	}{
		{"shift+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}},
		{"alt+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}},
		{"tab", key(tea.KeyTab)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turner := &recordingTurner{}
			m := newModel(t.Context(), turner, Config{Mode: permission.ModeManual})

			m = typed(m, "first line")
			m, cmd := m.press(tc.press)
			if cmd != nil {
				t.Fatal("the key that breaks a line started a turn")
			}
			m = typed(m, "second line")

			if m.input.text != want {
				t.Fatalf("the draft holds %q, want %q", m.input.text, want)
			}
			if len(turner.sent) != 0 {
				t.Fatalf("the turner was sent %v before Enter was pressed", turner.sent)
			}

			m, cmd = m.press(key(tea.KeyEnter))
			if cmd == nil {
				t.Fatal("Enter started no turn")
			}
			update(m, waitFor(t, run(cmd)))

			if len(turner.sent) != 1 || turner.sent[0] != want {
				t.Errorf("the turner was sent %q, want the whole draft as the one message %q",
					turner.sent, want)
			}
		})
	}
}

// TestAPastedDiffArrivesWholeAndSendsNothing. A bracketed paste reaches Update
// as its own message, so the newlines inside a patch are never delivered as the
// keypress that would have sent the line half-composed.
func TestAPastedDiffArrivesWholeAndSendsNothing(t *testing.T) {
	const patch = "@@ -12,5 +12,6 @@\n-\tclaims, err := parse(r.Header.Get(\"Authorization\"))\n" +
		"+\tclaims, err := m.claims(r)\n"
	const asked = "why does this hunk fail to apply? "

	turner := &recordingTurner{}
	m := newModel(t.Context(), turner, Config{Mode: permission.ModeManual})

	m = typed(m, asked)
	m = update(m, tea.PasteMsg{Content: patch})

	if len(turner.sent) != 0 {
		t.Fatalf("the paste sent %v by itself", turner.sent)
	}
	if m.input.text != asked+patch {
		t.Fatalf("the draft holds %q, want the pasted patch after what was typed", m.input.text)
	}

	m, cmd := m.press(key(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("Enter started no turn")
	}
	update(m, waitFor(t, run(cmd)))

	// Trimmed at the ends by begin (turn.go); the lines in the middle are the
	// pasted ones, and every one of them has to survive.
	if len(turner.sent) != 1 || turner.sent[0] != strings.TrimSpace(asked+patch) {
		t.Errorf("the turner was sent %q, want the typed line and the whole patch", turner.sent)
	}
}

// TestAPasteBringsItsLineEndingsIntoTheDraftsOwn. A terminal that took the text
// off a Windows clipboard, or that encodes the Enter inside a paste as a
// carriage return, sends CRLF or a bare CR — neither of which the model reads as
// a line break.
func TestAPasteBringsItsLineEndingsIntoTheDraftsOwn(t *testing.T) {
	m := newModel(t.Context(), &recordingTurner{}, Config{Mode: permission.ModeManual})
	m = update(m, tea.PasteMsg{Content: "one\r\ntwo\rthree\nfour"})

	if want := "one\ntwo\nthree\nfour"; m.input.text != want {
		t.Errorf("the draft holds %q, want %q", m.input.text, want)
	}
}

// TestTheFrameGrowsWithTheDraftAndTheFooterStaysUnderIt. The bottom chrome is
// built by appending, so a draft that took three lines and a frame that drew one
// would put the lower rule and the footer through the middle of what was typed.
func TestTheFrameGrowsWithTheDraftAndTheFooterStaysUnderIt(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	m.width = goldenWidth

	empty := m.View().Content
	m = typed(m, "one")
	m = update(m, tea.PasteMsg{Content: "\ntwo\nthree"})

	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	first := caretLine(t, lines)
	rule := strings.Repeat(frameRule, goldenWidth)

	if lines[first-1] != rule {
		t.Errorf("line %d reads %q, want the frame's upper rule", first-1, lines[first-1])
	}
	for i, want := range []string{"one", "two", "three"} {
		if !strings.Contains(lines[first+i], want) {
			t.Errorf("line %d of the draft reads %q, want it to carry %q", i, lines[first+i], want)
		}
	}
	if lines[first+3] != rule {
		t.Errorf("line %d reads %q, want the lower rule under all three lines of the draft",
			first+3, lines[first+3])
	}
	if first+4 != len(lines)-1 {
		t.Errorf("the footer is line %d of %d, and the rule under the draft is what it follows:\n%s",
			first+4, len(lines), strings.Join(lines, "\n"))
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, string(permission.ModeManual)) {
		t.Errorf("the last line of the frame is %q, and the footer is what ends it", last)
	}
	// Continuation lines are set in under the caret rather than opening a second
	// one: a caret per line would read as three prompts rather than one draft.
	if n := strings.Count(strings.Join(lines[first:first+3], "\n"), chat.Caret); n != 1 {
		t.Errorf("the three lines of the draft carry %d carets, want the one that opens them", n)
	}

	// And back: a draft emptied again draws the frame it started as, rather than
	// leaving the rows it grew standing.
	for range m.input.text {
		m, _ = m.press(key(tea.KeyBackspace))
	}
	if got := m.View().Content; got != empty {
		t.Errorf("a draft deleted back to nothing draws\n%s\nwant the frame it started as\n%s", got, empty)
	}
}

// TestTheCaretMovesAroundTheDraftFromTheKeyboard, which is what makes a line
// already typed correctable rather than only retypable.
func TestTheCaretMovesAroundTheDraftFromTheKeyboard(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	m.width = goldenWidth

	m = typed(m, "one")
	m, _ = m.press(key(tea.KeyTab))
	m = typed(m, "three")

	for _, press := range []tea.KeyPressMsg{key(tea.KeyUp), key(tea.KeyEnd)} {
		m, _ = m.press(press)
	}
	m = typed(m, " two")

	if want := "one two\nthree"; m.input.text != want {
		t.Fatalf("the draft holds %q, want %q — the correction landed somewhere else", m.input.text, want)
	}

	// Home, and the cursor lands on the first character of its own line rather
	// than of the draft — the only thing on screen saying where the next
	// keystroke goes (cursor.go).
	for _, press := range []tea.KeyPressMsg{key(tea.KeyDown), key(tea.KeyHome)} {
		m, _ = m.press(press)
	}
	if got, want := cursorCell(t, m), "t"; got != want {
		t.Errorf("the cursor stands on %q, want %q — the head of the second line", got, want)
	}

	// Left off the front of a line lands on the end of the one above it, and left
	// at the front of the draft has nowhere to go.
	m, _ = m.press(key(tea.KeyLeft))
	if want := len("one two"); m.input.at != want {
		t.Errorf("the caret is at byte %d, want %d — the end of the line above", m.input.at, want)
	}
	for range len(m.input.text) + 1 {
		m, _ = m.press(key(tea.KeyLeft))
	}
	if m.input.at != 0 {
		t.Errorf("the caret is at byte %d after being run off the front of the draft", m.input.at)
	}
}

// TestACommandIsTheFirstWordOfTheFirstLine. The fork between a command and a
// prompt reads the draft from its start (command.go), so a slash word further
// down one is prose — and a draft that opens with one is still a command however
// many lines follow it.
func TestACommandIsTheFirstWordOfTheFirstLine(t *testing.T) {
	t.Run("a slash word below the first line is prose", func(t *testing.T) {
		m := newModel(t.Context(), &recordingTurner{}, Config{Mode: permission.ModeManual})
		m = typed(m, "fix the auth test")
		m, _ = m.press(key(tea.KeyTab))
		m = typed(m, "/help is not what I mean here")

		m, cmd := m.press(key(tea.KeyEnter))
		if cmd == nil {
			t.Fatal("the draft was answered as a command instead of being sent")
		}
		if frame := words(m.View().Content); strings.Contains(frame, "list these commands") {
			t.Errorf("the command list was drawn for a draft that is a prompt:\n%s", frame)
		}
	})

	t.Run("a slash word opening the draft is a command", func(t *testing.T) {
		m := newModel(t.Context(), &recordingTurner{}, Config{Mode: permission.ModeManual})
		m = typed(m, "/help")
		m, _ = m.press(key(tea.KeyTab))
		m = typed(m, "and then some")

		m, cmd := m.press(key(tea.KeyEnter))
		if cmd != nil {
			t.Fatal("a draft opening with a slash word started a turn")
		}
		if frame := words(m.View().Content); !strings.Contains(frame, "list these commands") {
			t.Errorf("the command was not answered:\n%s", frame)
		}
	})
}

// TestAQuestionTakesTheNewlineKeyAndThePasteToo. The permission overlay takes
// the keyboard off the input line while it stands (prompt.go), and the keys this
// ticket added are keys like any other: a turn blocked on an answer has nowhere
// to send a line composed under it.
func TestAQuestionTakesTheNewlineKeyAndThePasteToo(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	m.permissions = &answers{decided: true}
	m, _ = m.ask(permission.Request{CallID: "call_1", Tool: "edit", Action: permission.ActionEdit, Path: "auth.go"})

	m = update(m, tea.PasteMsg{Content: "a patch\nwith two lines"})
	if m.input.text != "" {
		t.Errorf("a paste against a standing question was composed into %q", m.input.text)
	}
	m, _ = m.press(key(tea.KeyTab))
	if m.input.text != "" {
		t.Errorf("the newline key against a standing question was composed into %q", m.input.text)
	}
	if !m.asking() {
		t.Error("the question was closed by a key that answers nothing")
	}
}

// TestTheNewlineHintDropsBeforeTheInvitationDoes. The hint is advice about two
// keys and the placeholder is what a new session has to go on, so a terminal
// short of columns spends them on the placeholder — the same order the activity
// line drops its own hint in (activity.go).
func TestTheNewlineHintDropsBeforeTheInvitationDoes(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	m.width = goldenWidth

	if line := typedLineOf(t, m); !strings.Contains(line, inputHint) {
		t.Fatalf("the input line reads %q, and the hint is not on it", line)
	}

	// One column short of the two of them, which is the width the hint has to go
	// at and the placeholder has to survive.
	m.width = ansi.StringWidth(chat.Caret+placeholder) + hintGap + ansi.StringWidth(inputHint) - 1
	line := typedLineOf(t, m)
	if strings.Contains(line, inputHint) {
		t.Errorf("the hint is still drawn at %d columns: %q", m.width, line)
	}
	if !strings.Contains(line, placeholder) {
		t.Errorf("the invitation went before the hint did at %d columns: %q", m.width, line)
	}
	for _, drawn := range strings.Split(m.View().Content, "\n") {
		if n := ansi.StringWidth(drawn); n > m.width {
			t.Errorf("a line runs %d columns into a terminal %d wide: %q", n, m.width, drawn)
		}
	}

	// On the draft's last line rather than its first: it is advice about the key
	// under the reader's hands, and the caret is on that line.
	m.width = goldenWidth
	m = typed(m, "one")
	m, _ = m.press(key(tea.KeyTab))
	m = typed(m, "two")

	lines := strings.Split(ansi.Strip(m.typing()), "\n")
	if len(lines) != 2 {
		t.Fatalf("the input area drew %d line(s) for a draft of two:\n%s", len(lines), m.typing())
	}
	if strings.Contains(lines[0], inputHint) || !strings.Contains(lines[1], inputHint) {
		t.Errorf("the hint is not on the last line of the draft:\n%s", strings.Join(lines, "\n"))
	}
}

// caretLine is where in the frame the user types: the last line carrying a
// caret, since every prompt in the conversation above carries one too.
func caretLine(t *testing.T, lines []string) int {
	t.Helper()

	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimPrefix(lines[i], yoloCaret+" "), chat.Caret) {
			return i
		}
	}
	t.Fatalf("no line in the frame is the one being typed on:\n%s", strings.Join(lines, "\n"))
	return -1
}

// typedLineOf is that line, off a whole frame.
func typedLineOf(t *testing.T, m Model) string {
	t.Helper()

	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	return lines[caretLine(t, lines)]
}

// styleOf is text as this model's palette draws something faint, which is what
// a placeholder has to be rendered in to read as one.
func styleOf(m Model, text string) string {
	return styles.For(m.background).Faint.Render(text)
}
