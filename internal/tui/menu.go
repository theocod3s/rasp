package tui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/tui/styles"
)

// commandMenu is the completion menu's sticky half: whether "/" on an empty
// line has opened it and Esc has not yet closed it again, and which filtered
// entry Tab or an arrow has moved to. Whether it is actually showing, and what
// it is showing, are read off the draft each time instead (menuOpen,
// menuMatches) — a submit or a paste can move the draft out from under a
// cached answer, and a stale one would leave the menu open over a line it no
// longer describes, or closed over one it should be filtering.
type commandMenu struct {
	open     bool
	selected int
}

// menuHint replaces inputHint on the draft's last line while the menu is
// open: Tab and Enter mean something else there, and a hint still advertising
// "newline" and "send" would be lying about the keys under the reader's hands.
const menuHint = "↑↓⇥ select · ⏎ complete · esc close"

// menuQuery is the command name typed so far, and whether the draft is still
// shaped like one: starting the line, and not yet into arguments. Where that
// ends is unicode.IsSpace, parseCommand's own rule (command.go) — a menu that
// used a narrower one could keep filtering past the point submit already
// reads as the start of arguments, and complete a match Enter would not.
func menuQuery(text string) (query string, ok bool) {
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	name := text[1:]
	if strings.ContainsFunc(name, unicode.IsSpace) {
		return "", false
	}
	return name, true
}

// menuMatches is the commands whose name query prefixes, in the table's own
// order — the same order /help lists them in, so neither has a ranking of its
// own to drift out of step with the other's.
func menuMatches(query string) []command {
	var out []command
	for _, c := range commands() {
		if strings.HasPrefix(c.name, query) {
			out = append(out, c)
		}
	}
	return out
}

func (m Model) menuQuery() string {
	query, _ := menuQuery(m.input.text)
	return query
}

// menuOpen is whether the menu is drawn: opened and not dismissed, the draft
// still names a command that could be typed further, and completing it would
// still change something. An unambiguous exact match has nothing left to
// offer, so Enter goes back to meaning submit the moment the last letter of
// it lands — before the reader ever has to press anything to get there.
func (m Model) menuOpen() bool {
	if !m.menu.open {
		return false
	}
	query, ok := menuQuery(m.input.text)
	if !ok {
		return false
	}
	matches := menuMatches(query)
	return len(matches) != 1 || matches[0].name != query
}

// selected is the filtered list's own index for the entry Tab or an arrow
// last moved to, wrapped against the current filter rather than trusted
// outright: typing since the last move can only ever shrink the list a stale
// index was counted against.
func (m Model) selected(n int) int {
	if n == 0 {
		return 0
	}
	return m.menu.selected % n
}

// menuTracks is the one place every draft-mutating key runs through
// afterward: the selection goes back to the top of whatever the edit just
// changed the filter to, and an empty draft drops the sticky "opened" flag
// too. That second half matters more than it looks: without it, dispatching a
// command or backspacing to nothing leaves open() true with no session left
// to be open, and an unrelated paste landing on the empty line — which is
// never supposed to open the menu on its own (typeText) — inherits it and
// shows the menu anyway.
func (m Model) menuTracks() Model {
	m.menu.selected = 0
	if m.input.empty() {
		m.menu.open = false
	}
	return m
}

// menuClaims is whether Tab, an arrow or Enter answers to the menu rather
// than the draft. Enter only when there is a match to complete onto the line:
// with none, it falls through to submit, which is what answers "no such
// command" for a name the menu never suggested (command.go).
func (m Model) menuClaims(key tea.Key) bool {
	if key.Mod != 0 || !m.menuOpen() {
		return false
	}
	switch key.Code {
	case tea.KeyTab, tea.KeyUp, tea.KeyDown:
		return true
	case tea.KeyEnter:
		return len(menuMatches(m.menuQuery())) > 0
	}
	return false
}

func (m Model) menuPress(key tea.Key) Model {
	switch key.Code {
	case tea.KeyTab, tea.KeyDown:
		return m.menuMove(1)
	case tea.KeyUp:
		return m.menuMove(-1)
	case tea.KeyEnter:
		return m.menuComplete()
	}
	return m
}

// menuMove steps the selection by delta and wraps at either end: the menu is
// a ring because there is no "further" answer to give past its last entry.
func (m Model) menuMove(delta int) Model {
	matches := menuMatches(m.menuQuery())
	n := len(matches)
	if n == 0 {
		return m
	}
	m.menu.selected = (m.selected(n) + delta + n) % n
	return m
}

// menuComplete is Enter while the menu claims it: the selected command onto
// the input line with a trailing space, ready for arguments, and the menu
// closed. It fills the line rather than sending it — Enter answers to submit
// again from here, the same key an exact match already hands back to
// (menuOpen).
func (m Model) menuComplete() Model {
	matches := menuMatches(m.menuQuery())
	if len(matches) == 0 {
		return m
	}
	text := "/" + matches[m.selected(len(matches))].name + " "
	m.input = draft{text: text, at: len(text)}
	m.menu.open = false
	return m
}

// menuView draws the menu under the draft: every match in table order with
// the selected one marked, or — matching nothing — the sentence saying so
// rather than a box with nothing in it.
func (m Model) menuView() string {
	if !m.menuOpen() {
		return ""
	}

	query := m.menuQuery()
	matches := menuMatches(query)
	faint := styles.For(m.background).Faint
	if len(matches) == 0 {
		return menuLine(faint.Render("No command starts with /"+query+"."), m.width)
	}

	selected := m.selected(len(matches))
	width := commandWidth(matches)

	lines := make([]string, len(matches))
	for i, c := range matches {
		row := commandRow(c, width)
		marker := "  "
		if i == selected {
			marker = "> "
		} else {
			row = faint.Render(row)
		}
		lines[i] = menuLine(marker+row, m.width)
	}
	return strings.Join(lines, "\n")
}

// menuLine cuts a line to width rather than letting it wrap: a wrapped
// summary would read as a second, blank-markered entry under the one it
// belongs to.
func menuLine(line string, width int) string {
	if width > 0 && ansi.StringWidth(line) > width {
		return ansi.Truncate(line, width, "")
	}
	return line
}
