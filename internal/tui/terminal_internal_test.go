package tui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tool"
)

// TestWindowTitleSetAtStartAndRestoredOnQuit drives a real cursedRenderer —
// the golden suite disables it (harness_internal_test.go), so this is the one
// place the raw bytes Bubble Tea writes are ever inspected. It covers both
// ways a session ends: two Ctrl-C and /quit reach tea.Quit by different
// routes (model.go ctrlC, command.go leave), and both rely on the same
// shutdown to restore the title — Bubble Tea's own renderer clears it once
// View has carried a non-empty WindowTitle, and this is that claim proven
// rather than assumed.
func TestWindowTitleSetAtStartAndRestoredOnQuit(t *testing.T) {
	title := ansi.SetWindowTitle("rasp — /srv/rasp-demo")
	cleared := ansi.SetWindowTitle("")

	routes := []struct {
		name string
		quit func(*teatest.TestModel)
	}{
		{"ctrl-c twice", func(tm *teatest.TestModel) {
			tm.Send(ctrlCKey)
			tm.Send(ctrlCKey)
		}},
		{"slash quit", func(tm *teatest.TestModel) {
			tm.Type("/quit")
			tm.Send(key(tea.KeyEnter))
		}},
	}

	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			m := newModel(t.Context(), &promptTurner{}, Config{Cwd: "/srv/rasp-demo"})
			m.tty = true

			tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(goldenWidth, goldenHeight))
			route.quit(tm)

			out, err := io.ReadAll(tm.FinalOutput(t, teatest.WithFinalTimeout(settle)))
			if err != nil {
				t.Fatalf("reading the program's captured output: %v", err)
			}

			set := bytes.Index(out, []byte(title))
			clear := bytes.LastIndex(out, []byte(cleared))
			if set < 0 {
				t.Fatalf("the window title was never set on start:\n%q", out)
			}
			if clear < 0 {
				t.Fatalf("the window title was never restored on quit:\n%q", out)
			}
			if clear < set {
				t.Fatalf("the title was cleared before it was ever set:\n%q", out)
			}
		})
	}
}

// TestWindowTitleStaysOffWithoutATerminal is the degrade-silently half:
// nothing sets tty here, which is the state every model newModel builds is
// already in, so a program with no real terminal behind it must never write
// an OSC 2 sequence into a redirected file — set or cleared.
func TestWindowTitleStaysOffWithoutATerminal(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Cwd: "/srv/rasp-demo"})

	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(goldenWidth, goldenHeight))
	tm.Send(ctrlCKey)
	tm.Send(ctrlCKey)

	out, err := io.ReadAll(tm.FinalOutput(t, teatest.WithFinalTimeout(settle)))
	if err != nil {
		t.Fatalf("reading the program's captured output: %v", err)
	}
	if bytes.Contains(out, []byte("\x1b]2;")) {
		t.Fatalf("a window title sequence reached the output with no terminal behind the program:\n%q", out)
	}
}

// TestWindowTitleNeverEntersViewContent is what keeps the goldens out of this
// ticket's blast radius: the title travels on tea.View's own WindowTitle
// field, set below Content in View (model.go), and never as bytes inside it.
func TestWindowTitleNeverEntersViewContent(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Cwd: "/srv/rasp-demo"})
	m.tty = true

	v := m.View()
	if v.WindowTitle == "" {
		t.Fatal("View set no WindowTitle for a model on a real terminal")
	}
	if strings.Contains(v.Content, "\x1b]2;") {
		t.Fatalf("the window title escape sequence leaked into Content, which goldens compare:\n%q", v.Content)
	}
}

// TestBellRingsOnceWhenATurnEnds and TestBellRingsOnceWhenAQuestionOpens are
// the two moments design says are worth a sound (design's own comment on
// apply's EventTurnEnd case, prompt.go's ask). Both call apply/ask directly
// rather than through Update, so the command bell returns is checked without
// also depending on animate's own tea.Batch.
func TestBellRingsOnceWhenATurnEnds(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{})
	m.tty = true
	var rings int
	m.ring = func() { rings++ }

	_, cmd := m.apply(agent.Event{Kind: agent.EventTurnEnd})
	if cmd == nil {
		t.Fatal("EventTurnEnd returned no command, so the bell never rings")
	}
	cmd()
	if rings != 1 {
		t.Errorf("the bell rang %d time(s) for one turn ending, want 1", rings)
	}
}

func TestBellRingsOnceWhenAQuestionOpens(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{})
	m.permissions = &answers{decided: true}
	m.tty = true
	var rings int
	m.ring = func() { rings++ }

	_, cmd := m.ask(permission.Request{CallID: "call_1", Tool: "edit", Action: permission.ActionEdit, Path: "auth.go"})
	if cmd == nil {
		t.Fatal("a fresh question returned no command, so the bell never rings")
	}
	cmd()
	if rings != 1 {
		t.Errorf("the bell rang %d time(s) for one question opening, want 1", rings)
	}
}

// TestBellStaysSilentWithoutATerminal is the degrade-silently half for the
// bell: tty and ring are left at the zero values newModel leaves them, and
// neither moment above may hand back a command that would write into a
// redirected file.
func TestBellStaysSilentWithoutATerminal(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{})
	m.permissions = &answers{decided: true}

	if _, cmd := m.apply(agent.Event{Kind: agent.EventTurnEnd}); cmd != nil {
		t.Error("a turn ending returned a command with no terminal behind the program")
	}
	if _, cmd := m.ask(permission.Request{CallID: "call_1", Tool: "edit", Action: permission.ActionEdit,
		Path: "auth.go"}); cmd != nil {
		t.Error("a question opening returned a command with no terminal behind the program")
	}
}

// TestBellNeedsBothTTYAndRing pins bell's own guard rather than trusting
// Run's habit of setting the two together: either one missing must still
// stay silent, so a future caller that sets one without the other fails
// closed rather than writing into whatever ring happens to reach.
func TestBellNeedsBothTTYAndRing(t *testing.T) {
	base := newModel(t.Context(), &promptTurner{}, Config{})

	ringOnly := base
	ringOnly.ring = func() {}
	if cmd := ringOnly.bell(); cmd != nil {
		t.Error("bell fired with ring set but tty false")
	}

	ttyOnly := base
	ttyOnly.tty = true
	if cmd := ttyOnly.bell(); cmd != nil {
		t.Error("bell fired with tty true but no ring")
	}
}

// TestBellNeverRingsMidStreamOrOnATick is the negative space design calls out
// by name: every event a step reports before its own end, plus a tick of the
// activity line's own clock, must leave the bell silent — only the turn
// ending afterward may ring it.
func TestBellNeverRingsMidStreamOrOnATick(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{})
	m.tty = true
	var rings int
	m.ring = func() { rings++ }

	msg := &llm.Message{Content: []llm.Block{{Type: llm.BlockText, Text: "hi"}}}
	events := []tea.Msg{
		agentMsg{event: agent.Event{Kind: agent.EventAssistantDelta, Message: msg}},
		agentMsg{event: agent.Event{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"}},
		tickMsg{},
		agentMsg{event: agent.Event{Kind: agent.EventToolEnd, CallID: "call_1", Tool: "read",
			Result: &tool.Result{Content: "ok"}}},
		agentMsg{event: agent.Event{Kind: agent.EventAssistantEnd, Message: msg}},
	}

	for _, ev := range events {
		var cmd tea.Cmd
		m, cmd = m.route(ev)
		if cmd != nil {
			cmd()
		}
	}
	if rings != 0 {
		t.Fatalf("the bell rang %d time(s) before the turn ended", rings)
	}

	_, cmd := m.route(agentMsg{event: agent.Event{Kind: agent.EventTurnEnd}})
	if cmd == nil {
		t.Fatal("EventTurnEnd returned no command, so the bell never rings")
	}
	cmd()
	if rings != 1 {
		t.Errorf("the bell rang %d time(s) once the turn ended, want 1", rings)
	}
}

func TestIsTerminalIsFalseForARegularFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "not-a-terminal")
	if err != nil {
		t.Fatalf("creating a file to check: %v", err)
	}
	defer f.Close()

	if isTerminal(f) {
		t.Error("a plain file reported as a terminal, so a redirected run would write control sequences into it")
	}
}

func TestIsTerminalIsFalseForAPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("opening a pipe to check: %v", err)
	}
	defer r.Close()
	defer w.Close()

	if isTerminal(w) {
		t.Error("a pipe reported as a terminal, so a redirected run would write control sequences into it")
	}
}
