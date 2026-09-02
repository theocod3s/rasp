package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
)

// TestEveryCommandCanBeTypedAndAnswers is the table's own hygiene, and the one
// check that grows with it: a command nobody can spell, or one that changes
// nothing and returns nothing, is a keystroke the user watches vanish.
func TestEveryCommandCanBeTypedAndAnswers(t *testing.T) {
	list := commands()
	if len(list) == 0 {
		t.Fatal("there are no commands, and every check below would pass over an empty table")
	}

	seen := make(map[string]bool, len(list))
	for _, c := range list {
		t.Run(c.name, func(t *testing.T) {
			if c.run == nil {
				t.Fatal("the command has nothing to run, so Enter on it would panic")
			}
			if c.summary == "" {
				t.Fatal("the command has no summary, so /help lists it as a bare name")
			}
			if seen[c.name] {
				t.Fatalf("a second command is named %q, and dispatch answers whichever comes first", c.name)
			}
			seen[c.name] = true
			if name, _, ok := parseCommand("/" + c.name); !ok || name != c.name {
				t.Fatalf("/%s does not parse back to itself, so nothing typed can reach it", c.name)
			}

			m := newModel(t.Context(), &promptTurner{}, goldenConfig())
			held := m.chat.Len()

			next, cmd := m.dispatch(c.name, "")

			if cmd == nil && next.chat.Len() == held {
				t.Errorf("/%s left the conversation as it was and returned no command, "+
					"so pressing Enter on it did nothing the user can see", c.name)
			}
		})
	}
}

// TestHelpListsEveryCommand. /help is the only place the set of commands is
// visible, so a command missing from it is a command nobody finds.
func TestHelpListsEveryCommand(t *testing.T) {
	m, _ := newModel(t.Context(), &promptTurner{}, goldenConfig()).dispatch("help", "")

	frame := answered(m)
	for _, c := range commands() {
		if !strings.Contains(frame, "/"+c.name) {
			t.Errorf("/help does not list /%s:\n%s", c.name, frame)
		}
		if !strings.Contains(frame, words(c.summary)) {
			t.Errorf("/help lists /%s without saying what it does:\n%s", c.name, frame)
		}
	}
}

// TestAnUnknownCommandSaysSo. The failure this stands against is the quiet one:
// a mistyped command that draws nothing reads as a prompt that went missing,
// and the user waits for a reply to something nothing was ever sent.
func TestAnUnknownCommandSaysSo(t *testing.T) {
	m, cmd := newModel(t.Context(), &promptTurner{}, goldenConfig()).dispatch("serialise", "")

	if cmd != nil {
		t.Error("an unknown command returned something to run")
	}
	frame := answered(m)
	for _, want := range []string{"no /serialise command", "/help"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the answer to an unknown command does not mention %q:\n%s", want, frame)
		}
	}
}

// TestALineIsACommandOnlyWhenItsFirstWordIsOne. Every line the user sends
// passes through here, so the rule has to leave prose alone: a path, a fraction
// and a comment marker all carry slashes, and reading one as a command would
// eat the message.
func TestALineIsACommandOnlyWhenItsFirstWordIsOne(t *testing.T) {
	for _, tc := range []struct {
		line, name, args string
		command          bool
	}{
		{line: "/help", name: "help", command: true},
		{line: "/model anthropic/claude-opus-5", name: "model", args: "anthropic/claude-opus-5", command: true},
		{line: "/compact\tnow", name: "compact", args: "now", command: true},
		{line: "/set-effort high", name: "set-effort", args: "high", command: true},
		{line: "/usr/bin/env is missing"},
		{line: "look in /etc/hosts"},
		{line: "// TODO: fix the parser"},
		{line: "what is 1/2 of 8?"},
		{line: "/"},
		{line: ""},
	} {
		t.Run(tc.line, func(t *testing.T) {
			name, args, ok := parseCommand(tc.line)
			if ok != tc.command {
				t.Fatalf("parseCommand(%q) read it as a command=%v, want %v", tc.line, ok, tc.command)
			}
			if name != tc.name || args != tc.args {
				t.Errorf("parseCommand(%q) = (%q, %q), want (%q, %q)", tc.line, name, args, tc.name, tc.args)
			}
		})
	}
}

// TestEnterAnswersACommandAndSendsEverythingElse is the collision the rule
// above exists for, at the key that decides it: a command must never reach the
// model, and a prompt that merely contains a slash must still be sent.
func TestEnterAnswersACommandAndSendsEverythingElse(t *testing.T) {
	t.Run("a command", func(t *testing.T) {
		turner := &promptTurner{started: make(chan context.Context, 1)}
		m := typed(newModel(t.Context(), turner, goldenConfig()), "/help")

		m, cmd := m.press(key(tea.KeyEnter))

		if cmd != nil {
			t.Error("a command returned something to run; nothing but /quit has anything")
		}
		select {
		case <-turner.started:
			t.Fatal("the command was sent to the model as a prompt")
		default:
		}
		if m.input.text != "" {
			t.Errorf("the input still holds %q after the command was answered", m.input.text)
		}
		if m.busy {
			t.Error("a command left the UI saying a turn is running")
		}
		if frame := words(m.View().Content); !strings.Contains(frame, "Commands") {
			t.Errorf("Enter on /help drew no answer:\n%s", frame)
		}
	})

	t.Run("prose with a slash in it", func(t *testing.T) {
		const prompt = "look in /etc/hosts and say what owns it"

		turner := newTurner(nil)
		m := typed(newModel(t.Context(), turner, goldenConfig()), prompt)

		m, cmd := m.press(key(tea.KeyEnter))

		if cmd == nil {
			t.Fatal("Enter returned nothing, so the prompt was read as a command")
		}
		run(cmd)
		started(t, turner.started)
		if !m.busy {
			t.Error("the prompt was sent and the UI does not say a turn is running")
		}
	})
}

// TestQuitStopsTheRunningTurnBeforeItLeaves. /quit is ctrl+c spelled out, and
// the ordering is the part that matters: the turn is cancelled here, so it is
// already committing what it has while Bubble Tea shuts down and Run waits on
// it (tui.go).
func TestQuitStopsTheRunningTurnBeforeItLeaves(t *testing.T) {
	turner := newTurner(nil)
	m := typed(newModel(t.Context(), turner, goldenConfig()), "go")

	m, cmd := m.press(key(tea.KeyEnter))
	run(cmd)
	ctx := started(t, turner.started)

	m = typed(m, "/quit")
	_, quit := m.press(key(tea.KeyEnter))

	select {
	case <-ctx.Done():
	case <-time.After(settle):
		t.Error("/quit left the turn running, so the session ends with the turn abandoned mid-commit")
	}
	if quit == nil {
		t.Fatal("/quit returned no command, so the program keeps running")
	}
	if msg := quit(); !isQuit(msg) {
		t.Errorf("/quit returned a %T, want the command that quits", msg)
	}
}

// TestClearTakesTheConversationOffTheScreenAndKeepsWhatItCannotClear. The
// screen is all this package owns: the agent's transcript is reached through
// Send and nothing else, so a /clear that implied the model had forgotten
// anything would be a lie the next reply exposes.
func TestClearTakesTheConversationOffTheScreenAndKeepsWhatItCannotClear(t *testing.T) {
	m := drawnTurn(t, "Reading it now.")
	counted := m.status

	m, _ = m.dispatch("clear", "")

	frame := answered(m)
	if strings.Contains(frame, "Reading it now.") {
		t.Errorf("the cleared conversation is still on the screen:\n%s", frame)
	}
	if !strings.Contains(frame, "agent still has every message") {
		t.Errorf("the answer does not say the agent keeps the conversation it just took off the screen:\n%s", frame)
	}
	if m.chat.Len() != 1 {
		t.Errorf("the conversation holds %d items after a clear; the answer to /clear is the one of them", m.chat.Len())
	}
	if len(m.cards) != 0 {
		t.Errorf("%d tool card(s) survived the clear, and every one of them will be redrawn", len(m.cards))
	}
	// The counters are what the session has spent, and clearing the screen spends
	// nothing back: a turn after this one still pays for the messages behind it.
	if m.status != counted {
		t.Errorf("the status line went from %+v to %+v; a clear does not refund the tokens", counted, m.status)
	}
}

// TestATurnAfterAClearDrawsIntoTheEmptyConversation. Everything keyed by the
// conversation goes with it — the reply counter, the cards — and a key that
// outlived the items it named would put the next reply where the cleared one
// was, or draw it twice.
func TestATurnAfterAClearDrawsIntoTheEmptyConversation(t *testing.T) {
	m := drawnTurn(t, "Reading it now.")
	m, _ = m.dispatch("clear", "")

	next := reply("The header is parsed twice.")
	m = update(m, agentMsg{event: agent.Event{Kind: agent.EventAssistantDelta, Message: next}})
	m = update(m, agentMsg{event: agent.Event{Kind: agent.EventAssistantEnd, Message: next}})

	frame := answered(m)
	if n := strings.Count(frame, "The header is parsed twice."); n != 1 {
		t.Errorf("the reply after the clear is drawn %d time(s):\n%s", n, frame)
	}
	if m.chat.Len() != 2 {
		t.Errorf("the conversation holds %d items; the answer to /clear and the reply after it are two", m.chat.Len())
	}
}

// TestClearAndNewAreRefusedWhileATurnRuns. Both take the screen away, and the
// reply still arriving would go with it — so they say why rather than doing it
// or, worse, doing nothing.
func TestClearAndNewAreRefusedWhileATurnRuns(t *testing.T) {
	for _, name := range []string{"clear", "new"} {
		t.Run(name, func(t *testing.T) {
			turner := newTurner(nil)
			m := typed(newModel(t.Context(), turner, goldenConfig()), "go")
			m, cmd := m.press(key(tea.KeyEnter))
			run(cmd)
			started(t, turner.started)
			m = update(m, agentMsg{event: agent.Event{
				Kind: agent.EventAssistantDelta, Message: reply("Reading it now."),
			}})
			held := m.chat.Len()

			m, _ = m.dispatch(name, "")

			frame := answered(m)
			if !strings.Contains(frame, "Reading it now.") {
				t.Errorf("/%s cleared the turn that was still running:\n%s", name, frame)
			}
			if !strings.Contains(frame, "esc") {
				t.Errorf("/%s refused without saying how to stop the turn first:\n%s", name, frame)
			}
			if m.chat.Len() != held+1 {
				t.Errorf("the conversation holds %d items, want the %d it had plus the refusal", m.chat.Len(), held)
			}
		})
	}
}

// TestACommandThatCannotWorkYetSaysWhatItIsWaitingOn. Four of these are named
// for work that is not built, and the honest half of shipping them now is that
// each one names the piece it needs instead of failing silently or, worse,
// pretending it worked.
func TestACommandThatCannotWorkYetSaysWhatItIsWaitingOn(t *testing.T) {
	for _, tc := range []struct {
		label, name string
		cfg         Config
		want        []string
	}{
		{label: "model", name: "model", cfg: goldenConfig(),
			want: []string{goldenConfig().Model, "model catalog", "not built yet"}},
		{label: "model with none configured", name: "model", cfg: Config{},
			want: []string{"names no model"}},
		{label: "new", name: "new", cfg: goldenConfig(),
			want: []string{"session support", "carries this one on"}},
		{label: "resume", name: "resume", cfg: goldenConfig(),
			want: []string{"session store", "not built yet"}},
		{label: "compact", name: "compact", cfg: goldenConfig(),
			want: []string{"summarizer", "not built yet"}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			m, _ := newModel(t.Context(), &promptTurner{}, tc.cfg).dispatch(tc.name, "")

			frame := answered(m)
			for _, want := range tc.want {
				if !strings.Contains(frame, want) {
					t.Errorf("/%s does not mention %q:\n%s", tc.name, want, frame)
				}
			}
		})
	}
}

// drawnTurn is a finished turn on the screen: a reply, a tool card, and counts
// on the status line — real ones, because a line that already reads as zeros
// looks exactly the same after something zeroes it.
func drawnTurn(t *testing.T, text string) Model {
	t.Helper()

	used := llm.Usage{Input: 812, Output: 143, CacheRead: 11204}
	said := spent(reply(text), used)
	m := newModel(t.Context(), &promptTurner{}, goldenConfig())
	for _, ev := range []agent.Event{
		{Kind: agent.EventAssistantDelta, Message: said},
		{Kind: agent.EventAssistantEnd, Message: said},
		{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"},
		{Kind: agent.EventTurnEnd, Usage: used},
	} {
		m = update(m, agentMsg{event: ev})
	}

	if !strings.Contains(answered(m), text) {
		t.Fatalf("the turn never reached the screen, so there is nothing here to clear:\n%s", m.View().Content)
	}
	if m.busy {
		t.Fatal("the turn is still running, so a clear would be refused rather than tested")
	}
	if m.status.context == 0 || m.status.spent == (llm.Usage{}) {
		t.Fatalf("the turn put no counts on the status line (%+v), so a clear that reset them "+
			"would leave it looking exactly as it does now", m.status)
	}
	return m
}

// answered is the conversation alone, with the chrome around it left out. A
// whole frame is not evidence that a command's own answer said anything: the
// status line under it already carries the model's name and the session's
// counts, which is most of what there is to look for.
func answered(m Model) string { return words(m.chat.Render(goldenWidth)) }
