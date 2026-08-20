package tui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/tui/chat"
)

// command is one slash command. A table rather than a switch, so adding one —
// the picker for a setting, whatever a later milestone brings — is an entry and
// nothing else. run is never nil: an entry with nothing to run is the silent
// no-op this file exists to make impossible.
type command struct {
	name    string
	summary string
	run     func(Model, string) (Model, tea.Cmd)
}

// commands is every command rasp answers, in the order /help lists them.
//
// Four cannot yet do the job their name promises — the session store, the model
// catalog and the compactor are not built — and they answer by saying so, since
// a command that is not ready and a keystroke that vanished look identical from
// the other side of the screen. Each summary says what the command does *today*,
// so /help promises nothing the answer takes back.
func commands() []command {
	return []command{
		{"model", "show the model in use — switching needs the model catalog", showModel},
		{"new", "clear the screen; a conversation that starts over needs sessions", startNew},
		{"resume", "reopen an earlier session — needs session support", resume},
		{"compact", "summarize the conversation to free context — needs compaction", compact},
		{"clear", "clear the conversation off the screen", clearConversation},
		{"yolo", "run every tool call with no approval — /yolo confirm turns it on", yolo},
		{"help", "list these commands", help},
		{"quit", "stop a running turn and leave", leave},
	}
}

// parseCommand reads a submitted line as a slash command: the name after the
// slash, and whatever follows the first space.
//
// A line is a command only when its first word is a slash and one plain word of
// letters, digits, hyphens and underscores, which is what keeps prose carrying a
// slash prose: `/usr/bin/env is missing` has a second slash in its first word,
// and a line whose slash is anywhere but the front never gets this far. The
// trade is deliberate and has another side — `/tmp is full` is read as a command
// — so the answer to an unknown one has to say what happened.
func parseCommand(line string) (name, args string, ok bool) {
	if !strings.HasPrefix(line, "/") {
		return "", "", false
	}
	name, args = line[1:], ""
	if i := strings.IndexFunc(name, unicode.IsSpace); i >= 0 {
		name, args = name[:i], strings.TrimSpace(name[i:])
	}
	if name == "" || strings.IndexFunc(name, func(r rune) bool { return !plain(r) }) >= 0 {
		return "", "", false
	}
	return name, args, true
}

func plain(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
}

// submit is Enter: a command, or a prompt for the model.
//
// The fork sits ahead of begin's own guards because a command is not a turn.
// /quit has to reach a running turn in order to stop it, and the rest have to
// answer rather than be swallowed by the check that holds a second prompt back
// until the first turn ends (turn.go).
func (m Model) submit() (Model, tea.Cmd) {
	if name, args, ok := parseCommand(strings.TrimSpace(m.input)); ok {
		m.input = ""
		return m.dispatch(name, args)
	}
	return m.begin()
}

// dispatch answers a command, including one no entry matches: a typo that drew
// nothing reads as a message that went missing, and leaves the user waiting for
// a reply to a prompt nothing was ever sent.
func (m Model) dispatch(name, args string) (Model, tea.Cmd) {
	for _, c := range commands() {
		if c.name == name {
			return c.run(m, args)
		}
	}
	return m.say("There is no /" + name + " command. /help lists the ones there are."), nil
}

func (m Model) say(text string) Model {
	m.chat.Append(chat.Notice{Text: text, Background: m.background})
	return m
}

func showModel(m Model, _ string) (Model, tea.Cmd) {
	using := "This session names no model."
	if m.status.model != "" {
		using = "This session is using " + m.status.model + "."
	}
	return m.say(using + " Switching model mid-session needs the model catalog, which is not built yet."), nil
}

func startNew(m Model, _ string) (Model, tea.Cmd) {
	if m.busy {
		return m.say(running("/new")), nil
	}
	return m.forget().say("Cleared the screen. A conversation that genuinely starts over — its own " +
		"transcript, a context beginning empty — arrives with session support; until then the agent " +
		"carries this one on."), nil
}

func clearConversation(m Model, _ string) (Model, tea.Cmd) {
	if m.busy {
		return m.say(running("/clear")), nil
	}
	return m.forget().say("Cleared the conversation off the screen. The agent still has every message " +
		"in it — clearing what the model is sent arrives with session support."), nil
}

func resume(m Model, _ string) (Model, tea.Cmd) {
	return m.say("Reopening an earlier session needs the session store, which is not built yet: " +
		"nothing is written to disk, so there is nothing to list."), nil
}

func compact(m Model, _ string) (Model, tea.Cmd) {
	return m.say("Compacting needs the summarizer, which is not built yet. Until it lands, a " +
		"conversation that fills the context window has to be started again rather than shortened."), nil
}

func help(m Model, _ string) (Model, tea.Cmd) {
	list := commands()
	var width int
	for _, c := range list {
		width = max(width, len(c.name))
	}

	var b strings.Builder
	b.WriteString("Commands")
	for _, c := range list {
		b.WriteString("\n  /" + c.name + strings.Repeat(" ", width-len(c.name)+2) + c.summary)
	}
	return m.say(b.String()), nil
}

// leave quits, in ctrl+c's order: the turn is already unwinding while Bubble Tea
// shuts the event loop down, and Run waits on it before returning (tui.go).
func leave(m Model, _ string) (Model, tea.Cmd) {
	m.interrupt()
	return m, tea.Quit
}

// forget drops everything the UI holds about the conversation on screen — and
// only that. The agent's transcript is not this package's to touch: the UI
// reaches the agent through Send and nothing else (design §6), so the next
// request carries what it always would, which is why both commands calling this
// say so rather than claiming the conversation is gone.
//
// The status line is left alone for the same reason. Its counts are what the
// session has spent, and a turn after this one still pays for the messages
// behind it; zeroing them puts a wrong number where a reader looks for the bill
// (status.go).
func (m Model) forget() Model {
	m.chat = chat.View{}
	m.cards = nil
	m.replies = 0
	m.streaming = nil
	m.err = nil
	return m
}

func running(name string) string {
	return name + " needs the running turn to be over first — press esc twice to stop it. Clearing " +
		"now would take the reply still arriving off the screen with everything else."
}
