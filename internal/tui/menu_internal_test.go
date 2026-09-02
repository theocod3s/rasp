package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestSlashOnAnEmptyLineOpensTheMenuListingEveryCommand is command.go's own
// hygiene test (TestHelpListsEveryCommand), aimed at the menu instead: both
// draw off commands() and neither keeps a second copy of it, so a command
// added, renamed or resummarised has to show up here without being told by
// hand.
func TestSlashOnAnEmptyLineOpensTheMenuListingEveryCommand(t *testing.T) {
	m := typed(newModel(t.Context(), &promptTurner{}, goldenConfig()), "/")

	if !m.menuOpen() {
		t.Fatal("/ on an empty line did not open the menu")
	}

	view := words(m.menuView())
	for _, c := range commands() {
		if !strings.Contains(view, "/"+c.name) {
			t.Errorf("the menu does not list /%s:\n%s", c.name, view)
		}
		if !strings.Contains(view, words(c.summary)) {
			t.Errorf("the menu lists /%s without its summary:\n%s", c.name, view)
		}
	}
	if n := len(strings.Split(strings.TrimSpace(m.menuView()), "\n")); n != len(commands()) {
		t.Errorf("the menu draws %d line(s) for %d commands, so it is not exactly the table", n, len(commands()))
	}
}

// TestSlashMidSentenceNeverOpensTheMenu. A slash anywhere but the front of an
// empty line is prose to parseCommand (command.go), and the menu has to read
// it the same way or a path, a fraction or a comment marker would pop it open
// mid-sentence.
func TestSlashMidSentenceNeverOpensTheMenu(t *testing.T) {
	m := typed(newModel(t.Context(), &promptTurner{}, goldenConfig()), "look in /etc/hosts")

	if m.menuOpen() {
		t.Error("a / that was not the first character of an empty line opened the menu")
	}
	if m.input.text != "look in /etc/hosts" {
		t.Errorf("the slash mid-sentence was not composed into the draft: %q", m.input.text)
	}
}

// TestTypingFiltersTheMenu.
func TestTypingFiltersTheMenu(t *testing.T) {
	m := typed(newModel(t.Context(), &promptTurner{}, goldenConfig()), "/c")

	if !m.menuOpen() {
		t.Fatal("the menu closed while still filtering to more than one command")
	}
	view := words(m.menuView())
	for _, want := range []string{"/compact", "/clear"} {
		if !strings.Contains(view, want) {
			t.Errorf("filtering to \"c\" dropped %s:\n%s", want, view)
		}
	}
	for _, name := range []string{"/model", "/effort", "/new", "/resume", "/yolo", "/help", "/quit"} {
		if strings.Contains(view, name) {
			t.Errorf("filtering to \"c\" still shows %s:\n%s", name, view)
		}
	}
}

// TestAFilterMatchingNothingSaysSo is the criterion this ticket names by
// itself: a filter with no matches has to say so, not draw a box with nothing
// in it.
func TestAFilterMatchingNothingSaysSo(t *testing.T) {
	m := typed(newModel(t.Context(), &promptTurner{}, goldenConfig()), "/zzz")

	if !m.menuOpen() {
		t.Fatal("the menu closed on a filter that matched nothing, rather than saying so")
	}
	view := m.menuView()
	if strings.TrimSpace(view) == "" {
		t.Fatal("a filter matching nothing drew an empty box")
	}
	if !strings.Contains(words(view), "No command starts with /zzz") {
		t.Errorf("the empty filter does not say so:\n%s", view)
	}
}

// TestEnterCompletesTheSelectedCommandOntoTheInputLine. Completion fills the
// line; it does not send it — a second Enter, with the menu now closed,
// submits it the ordinary way.
func TestEnterCompletesTheSelectedCommandOntoTheInputLine(t *testing.T) {
	m := typed(newModel(t.Context(), &promptTurner{}, goldenConfig()), "/e")

	m, cmd := m.press(key(tea.KeyEnter))

	if cmd != nil {
		t.Error("completing a command from the menu started a turn")
	}
	if m.menuOpen() {
		t.Error("the menu is still open after Enter completed its selection")
	}
	if m.input.text != "/effort " {
		t.Errorf("the input line holds %q, want the completed command with a trailing space", m.input.text)
	}
}

// TestEnterOnAFilterMatchingNothingSubmitsItAsAnUnknownCommand. With nothing
// for the menu to complete, Enter falls through to submit — the same "no such
// command" answer typing it with the menu closed already gives
// (TestAnUnknownCommandSaysSo, command_internal_test.go).
func TestEnterOnAFilterMatchingNothingSubmitsItAsAnUnknownCommand(t *testing.T) {
	turner := &promptTurner{started: make(chan context.Context, 1)}
	m := typed(newModel(t.Context(), turner, goldenConfig()), "/zzz")

	m, cmd := m.press(key(tea.KeyEnter))

	if cmd != nil {
		t.Error("an unknown command returned something to run")
	}
	if m.input.text != "" {
		t.Errorf("the input still holds %q after an unrecognised command was submitted", m.input.text)
	}
	if frame := answered(m); !strings.Contains(frame, "no /zzz command") {
		t.Errorf("the frame does not say /zzz is unknown:\n%s", frame)
	}
}

// TestTabAndArrowsMoveTheMenusSelectionAndWrap.
func TestTabAndArrowsMoveTheMenusSelectionAndWrap(t *testing.T) {
	m := typed(newModel(t.Context(), &promptTurner{}, goldenConfig()), "/")
	n := len(commands())

	for i := 1; i < n; i++ {
		m, _ = m.press(key(tea.KeyTab))
		if m.menu.selected != i {
			t.Fatalf("after %d Tab press(es) the selection is %d, want %d", i, m.menu.selected, i)
		}
	}
	m, _ = m.press(key(tea.KeyTab))
	if m.menu.selected != 0 {
		t.Errorf("Tab past the last entry landed on %d, want it to wrap to 0", m.menu.selected)
	}

	m, _ = m.press(key(tea.KeyUp))
	if m.menu.selected != n-1 {
		t.Errorf("Up from the first entry landed on %d, want it to wrap to %d", m.menu.selected, n-1)
	}
	m, _ = m.press(key(tea.KeyDown))
	if m.menu.selected != 0 {
		t.Errorf("Down from the wrapped selection landed on %d, want back to 0", m.menu.selected)
	}
}

// TestTabMovesTheSelectionWhenTheMenuIsOpenAndBreaksALineOtherwise is the Tab
// resolution this ticket settles: the menu's keyboard claim outranks
// breaksLine's while it is open, and Tab goes back to breaking a line the
// moment it is not (model.go's breaksLine).
func TestTabMovesTheSelectionWhenTheMenuIsOpenAndBreaksALineOtherwise(t *testing.T) {
	m := typed(newModel(t.Context(), &promptTurner{}, goldenConfig()), "/")
	if !m.menuOpen() {
		t.Fatal("the menu never opened")
	}

	m, _ = m.press(key(tea.KeyTab))
	if strings.Contains(m.input.text, "\n") {
		t.Errorf("Tab broke a line while the menu was open: %q", m.input.text)
	}
	if m.menu.selected != 1 {
		t.Error("Tab did not move the menu's selection")
	}

	m, _ = m.press(key(tea.KeyEscape))
	if m.menuOpen() {
		t.Fatal("Esc did not close the menu")
	}

	m, _ = m.press(key(tea.KeyTab))
	if m.input.text != "/\n" {
		t.Errorf("Tab with the menu closed did not insert a newline: %q", m.input.text)
	}
}

// TestEscClosesTheMenuAndReturnsToNormalTyping. The draft is left exactly as
// it stood — closing is not clearing — and typing afterward composes the line
// the way it always does, without the menu reopening on its own.
func TestEscClosesTheMenuAndReturnsToNormalTyping(t *testing.T) {
	m := typed(newModel(t.Context(), &promptTurner{}, goldenConfig()), "/mo")
	if !m.menuOpen() {
		t.Fatal("the menu never opened")
	}

	m, _ = m.press(key(tea.KeyEscape))

	if m.menuOpen() {
		t.Error("Esc did not close the menu")
	}
	if m.input.text != "/mo" {
		t.Errorf("Esc changed what was typed: %q", m.input.text)
	}

	m = typed(m, "del")
	if m.input.text != "/model" {
		t.Errorf("typing after Esc was not composed normally: %q", m.input.text)
	}
	if m.menuOpen() {
		t.Error("the menu reopened on its own once the draft matched a command again")
	}
}

// TestTheMenuNeverFightsThePermissionOverlayForTheKeyboard. A question can
// open while the menu is still up — the user typing ahead of a running turn
// that then asks for approval — and every key the menu would otherwise claim
// has to reach the overlay untouched instead (prompt.go's own asking()).
func TestTheMenuNeverFightsThePermissionOverlayForTheKeyboard(t *testing.T) {
	ans := &answers{decided: true}
	c := newClock(goldenNow)
	m := newModel(t.Context(), &promptTurner{}, goldenConfig())
	m.now = c.read
	m.permissions = ans

	m = typed(m, "/")
	if !m.menuOpen() {
		t.Fatal("the menu never opened, so this test exercises nothing")
	}

	m = update(m, promptMsg{request: editRequest()})
	if !m.asking() {
		t.Fatal("the question never opened")
	}
	c.pass(promptGrace)

	selected, text := m.menu.selected, m.input.text
	for _, press := range []tea.KeyPressMsg{
		key(tea.KeyTab), key(tea.KeyDown), key(tea.KeyUp), key(tea.KeyEnter), key(tea.KeyEscape),
	} {
		m, _ = m.press(press)
	}

	if !m.asking() {
		t.Error("a key meant for the menu closed or answered the standing question instead")
	}
	if m.menu.selected != selected {
		t.Errorf("the menu's selection moved to %d while a question stood, want %d unchanged",
			m.menu.selected, selected)
	}
	if m.input.text != text {
		t.Errorf("the draft changed to %q while a question stood, want %q unchanged", m.input.text, text)
	}
}

// TestAPasteNeverOpensTheMenuEvenAfterADispatchedCommand. typeText's own rule
// is that a paste never opens the menu on its own, and a stale open() left
// over from a session that already dispatched is the same thing wearing a
// different keystroke: the menu must not reappear just because the draft it
// lands on happens to look like a command again.
func TestAPasteNeverOpensTheMenuEvenAfterADispatchedCommand(t *testing.T) {
	m := typed(newModel(t.Context(), &promptTurner{}, goldenConfig()), "/zzz")
	m, _ = m.press(key(tea.KeyEnter))
	if m.input.text != "" || m.menuOpen() {
		t.Fatalf("the unknown command did not submit cleanly: text=%q open=%v", m.input.text, m.menuOpen())
	}

	m = update(m, tea.PasteMsg{Content: "/c"})

	if m.menuOpen() {
		t.Error("a paste after a dispatched command reopened the menu on its own")
	}
}

// TestAPasteNeverOpensTheMenuAfterBackspacingToEmpty. The same rule from the
// other route to an empty draft: backspacing the menu's own trigger away
// rather than sending it.
func TestAPasteNeverOpensTheMenuAfterBackspacingToEmpty(t *testing.T) {
	m := typed(newModel(t.Context(), &promptTurner{}, goldenConfig()), "/")
	m, _ = m.press(key(tea.KeyBackspace))
	if m.input.text != "" || m.menuOpen() {
		t.Fatalf("backspacing the trigger away did not leave an empty, closed draft: text=%q open=%v",
			m.input.text, m.menuOpen())
	}

	m = update(m, tea.PasteMsg{Content: "/etc"})

	if m.menuOpen() {
		t.Error("a paste after backspacing to empty reopened the menu on its own")
	}
}

// TestMenuQueryEndsAtTheSameSpaceParseCommandDoes. The menu and submit have to
// agree on where the command name ends, or the menu can say "no such command"
// over a name Enter goes on to dispatch anyway (or the reverse).
// parseCommand's own rule is unicode.IsSpace (command.go); U+00A0 (NBSP) is
// one such space that " \t\n" alone misses.
func TestMenuQueryEndsAtTheSameSpaceParseCommandDoes(t *testing.T) {
	const text = "/help\u00A0now"

	if _, ok := menuQuery(text); ok {
		t.Fatalf("menuQuery(%q) read a unicode space as part of the command name", text)
	}
	if name, args, ok := parseCommand(text); !ok || name != "help" || args != "now" {
		t.Fatalf("parseCommand(%q) = (%q, %q, %v), want (\"help\", \"now\", true) — "+
			"this test's premise is that submit already treats that space as the end of the name",
			text, name, args, ok)
	}
}

// TestAnUnhandledKeyWithNoTextLeavesTheMenuAlone. A key press's() only routes
// to typeText for what it does not otherwise bind, and one that carries no
// text — an unmapped function key or ctrl-chord — is not a keystroke that
// changed anything the menu filters on, so it must not reset the selection
// either.
func TestAnUnhandledKeyWithNoTextLeavesTheMenuAlone(t *testing.T) {
	m := typed(newModel(t.Context(), &promptTurner{}, goldenConfig()), "/")
	m, _ = m.press(key(tea.KeyTab))
	if m.menu.selected != 1 {
		t.Fatalf("Tab did not move the selection to 1 first: got %d", m.menu.selected)
	}

	m, _ = m.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})

	if m.menu.selected != 1 {
		t.Errorf("an unhandled key with no text reset the selection to %d", m.menu.selected)
	}
	if m.input.text != "/" {
		t.Errorf("an unhandled key with no text changed the draft to %q", m.input.text)
	}
}

// TestTheHintNamesTheMenusKeysWhileItIsOpen. inputHint promises "newline" and
// "send", and neither Tab nor Enter does that while the menu answers to them
// instead — so the hint line has to say what they do now.
func TestTheHintNamesTheMenusKeysWhileItIsOpen(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, goldenConfig())
	m.width = goldenWidth

	if !strings.Contains(m.typing(), inputHint) {
		t.Fatal("the ordinary hint is not drawn with no menu open")
	}

	m = typed(m, "/")
	line := m.typing()
	if strings.Contains(line, inputHint) {
		t.Errorf("the newline-and-send hint is still drawn while the menu is open: %q", line)
	}
	if !strings.Contains(line, menuHint) {
		t.Errorf("the menu's own hint is not drawn while it is open: %q", line)
	}
}
