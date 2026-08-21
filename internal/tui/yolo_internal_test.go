package tui

import (
	"errors"
	"strconv"
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

// TestArmingTheBypassTakesTheSecondLine. Every permission prompt in the session
// goes away on this command, and the three keys that answer one of those prompts
// are a single press each — so the command that switches them all off asks for
// something a hand cannot land on by accident.
func TestArmingTheBypassTakesTheSecondLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		arms bool
	}{
		{name: "the command on its own", line: "/yolo"},
		{name: "a word that is not the confirmation", line: "/yolo yes"},
		{name: "the confirmation with a word after it", line: "/yolo confirm now"},
		{name: "the confirmation", line: "/yolo " + yoloConfirm, arms: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &answers{}
			m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
			m.permissions = service

			m = typeCommand(m, tc.line)

			if service.armed() != tc.arms {
				t.Errorf("the service was told %v, want the bypass armed = %v", service.yolos, tc.arms)
			}
			if m.status.yolo != tc.arms {
				t.Errorf("the status line has the badge = %v, want %v", m.status.yolo, tc.arms)
			}

			frame := words(m.View().Content)
			if tc.arms {
				if !strings.Contains(frame, yoloBadge) {
					t.Errorf("the frame does not carry the badge:\n%s", frame)
				}
				return
			}
			// The command that did not arm it has to say what it wants, or it reads
			// as a command that silently failed — and what the user does next is
			// press it again.
			if !strings.Contains(frame, words(yoloWarning)) {
				t.Errorf("the frame does not say what arming would cost or how to ask for it:\n%s", frame)
			}
		})
	}
}

// TestLeavingTheBypassTakesNothing is the deliberate asymmetry: /yolo on an
// armed session turns it off on the spot. Confirming a step back towards the
// guardrails would be asking the user to insist on being protected.
func TestLeavingTheBypassTakesNothing(t *testing.T) {
	service := &answers{}
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModePlan})
	m.permissions = service

	m = typeCommand(m, "/yolo "+yoloConfirm)
	if !service.armed() {
		t.Fatal("the bypass was never armed, so turning it off proves nothing")
	}

	m = typeCommand(m, "/yolo")

	if service.armed() {
		t.Errorf("the service was told %v, want the bypass off", service.yolos)
	}
	if m.status.yolo {
		t.Error("the badge is still on the status line")
	}
	// And the session says which mode it is gated by again, rather than leaving
	// the user to work out what the badge was hiding.
	if frame := words(m.View().Content); !strings.Contains(frame, string(permission.ModePlan)) {
		t.Errorf("nothing on the screen names the mode the session came back to:\n%s", frame)
	}
}

// TestTheConfirmationNeverTurnsItOff. `/yolo confirm` is what /help and the
// warning both say turns it on, so someone who armed it with --yolo — and so
// never read either — must not turn it off by typing the words that mean on.
func TestTheConfirmationNeverTurnsItOff(t *testing.T) {
	service := &answers{}
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	m.permissions = service

	m = typeCommand(m, "/yolo "+yoloConfirm)
	if !service.armed() {
		t.Fatal("the bypass was never armed, so the second command below proves nothing")
	}

	m = typeCommand(m, "/yolo "+yoloConfirm)

	if !service.armed() {
		t.Errorf("the service was told %v; the confirmation turned the bypass off", service.yolos)
	}
	if !m.status.yolo {
		t.Error("the badge went out on a command that says it turns the bypass on")
	}
	if frame := words(m.View().Content); !strings.Contains(frame, "already on") {
		t.Errorf("nothing on the screen says the command changed nothing:\n%s", frame)
	}

	// And the one word out still works.
	m = typeCommand(m, "/yolo")
	if service.armed() || m.status.yolo {
		t.Errorf("bare /yolo left the bypass on: %v", service.yolos)
	}
}

// TestTheBadgeIsOnEveryFrameWhileTheBypassIsArmed. A badge that goes missing in
// the states nobody looks at is the one thing this indicator may not do: it is
// the only thing on the screen saying that nothing will be asked before anything
// runs (design §7.8).
func TestTheBadgeIsOnEveryFrameWhileTheBypassIsArmed(t *testing.T) {
	asked := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockText, Text: "Reading it."},
		{Type: llm.BlockToolUse, ID: "call_1", Name: "read"},
	}}

	for _, tc := range []struct {
		name   string
		width  int
		events []agent.Event
	}{
		{name: "nothing has happened since", width: goldenWidth},
		{name: "before the terminal has reported a size"},
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
			m := newModel(t.Context(), newTurner(nil), Config{
				Model: "anthropic/claude-opus-5",
				Mode:  permission.ModeManual,
			})
			m.permissions = &answers{}
			m = typeCommand(m, "/yolo "+yoloConfirm)
			m = update(m, tea.WindowSizeMsg{Width: tc.width, Height: goldenHeight})
			for _, ev := range tc.events {
				m = update(m, agentMsg{event: ev})
			}

			if frame := words(m.View().Content); !strings.Contains(frame, yoloBadge) {
				t.Errorf("the frame carries no badge:\n%s", frame)
			}
		})
	}
}

// TestTheLineBeingTypedOnSaysItToo. A status line is one line among many on a
// full screen and is scrolled past; the caret is where the eyes are while
// typing, which is why the indicator is on both (design §7.8).
func TestTheLineBeingTypedOnSaysItToo(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	m.permissions = &answers{}
	m.width = goldenWidth

	if before := typedLineOf(t, m); strings.Contains(before, yoloCaret) {
		t.Fatalf("a gated session already marks its input line %q, so the change below says nothing", before)
	}

	m = typeCommand(m, "/yolo "+yoloConfirm)
	m = typed(m, "rm the lot")

	line := typedLineOf(t, m)
	if !strings.Contains(line, yoloCaret) {
		t.Errorf("the line being typed on is %q, and nothing on it says the approvals are off", line)
	}
	if !strings.Contains(line, "rm the lot") {
		t.Errorf("the line being typed on is %q, and what was typed is not on it", line)
	}
	// Marked rather than replaced: the caret is where the cursor sits, and a
	// prompt that lost it would move the text the user is editing.
	if !strings.Contains(line, chat.Caret) {
		t.Errorf("the caret is gone from %q", line)
	}
	// And the mark is inside the frame with the caret, not on a line of its own
	// above it (input.go).
	if !strings.HasPrefix(line, yoloCaret+" "+chat.Caret) {
		t.Errorf("the line being typed on opens %q, want the mark and then the caret", line)
	}
}

// TestTheBadgeStandsWhereTheModeNameWould. Under the bypass no preset is
// consulted at all, so a line still reading "plan" beside the badge would name
// rules nothing is running — and the mode is the segment a reader trusts.
func TestTheBadgeStandsWhereTheModeNameWould(t *testing.T) {
	line := ansi.Strip(status{mode: permission.ModePlan, yolo: true}.Render(0, styles.Dark))

	if !strings.HasPrefix(line, yoloBadge) {
		t.Errorf("the line opens %q, want the badge first", line)
	}
	if strings.Contains(line, string(permission.ModePlan)) {
		t.Errorf("the line names the mode the bypass is answering ahead of: %q", line)
	}

	// Inverse video rather than a colour, so it stays loud on a terminal whose
	// theme this build knows nothing about (status.go).
	drawn := (status{yolo: true}).Render(0, styles.Dark)
	if !strings.Contains(drawn, "\x1b[1;7m") {
		t.Errorf("the badge is drawn %q, without the reverse attribute", drawn)
	}
}

// TestTheBadgeSurvivesEveryDrop. A narrow terminal is where the line has least
// room and the badge matters most, so it goes last of everything on it.
func TestTheBadgeSurvivesEveryDrop(t *testing.T) {
	s := status{
		model:   "anthropic/claude-opus-5",
		mode:    permission.ModeManual,
		yolo:    true,
		context: 12_800,
		spent:   llm.Usage{Input: 2342, Output: 205, CacheRead: 22408},
	}

	for _, width := range []int{0, 80, 40, 20, 8} {
		t.Run(strconv.Itoa(width)+" columns", func(t *testing.T) {
			line := ansi.Strip(s.Render(width, styles.Dark))
			if !strings.HasPrefix(line, yoloBadge) {
				t.Errorf("at %d columns the line reads %q, and the badge is not the whole of what is left",
					width, line)
			}
			if width > 0 && ansi.StringWidth(line) > width {
				t.Errorf("the line is %d columns wide in a terminal %d wide", ansi.StringWidth(line), width)
			}
		})
	}
}

// TestArmingRidesInOnTheNextTurn. The model is the one party that cannot see the
// badge, and a model that does not know the prompts are gone is a model still
// pacing itself for a user who is no longer being asked.
func TestArmingRidesInOnTheNextTurn(t *testing.T) {
	turner := &recordingTurner{}
	m := newModel(t.Context(), turner, Config{Mode: permission.ModeManual})
	m.permissions = &answers{}

	m = typeCommand(m, "/yolo "+yoloConfirm)
	m = sendLine(t, m, "carry on")

	if len(turner.sent) != 1 {
		t.Fatalf("the turner was sent %v, want the one turn", turner.sent)
	}
	if !strings.HasPrefix(turner.sent[0], yoloReminder(true, permission.ModeManual)) {
		t.Errorf("the turn carried %q; it has to open with what arming told the model", turner.sent[0])
	}
}

// TestNoAmountOfCyclingArmsTheBypass is the other half of the cycle's guarantee
// (mode_internal_test.go): the rotation cannot name yolo, and neither can it
// reach the bypass by any other route — there is nothing in the key's path that
// arms one.
func TestNoAmountOfCyclingArmsTheBypass(t *testing.T) {
	const presses = 3*len(cycleModes) + 1

	service := &answers{}
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	m.permissions = service

	for range presses {
		m = update(m, shiftTab)
	}

	if len(service.modes) != presses {
		t.Fatalf("%d press(es) reached the service out of %d; the rest of this test would be "+
			"examining nothing", len(service.modes), presses)
	}
	if service.armed() {
		t.Errorf("the service was told %v; no number of presses may arm the bypass", service.yolos)
	}
	if m.status.yolo {
		t.Error("the status line drew the badge for a session nothing armed")
	}
}

// TestCyclingOutOfTheBypassGatesTheSessionAgain runs the whole of leaving
// against a real service. Two things have to hold and neither is visible from
// the other: the press lands in manual rather than the next mode along — the
// mode under an armed bypass is not what the session is running, so advancing it
// would be advancing a name nothing enforces — and the ladder is really back in
// the way, which only the service can say.
func TestCyclingOutOfTheBypassGatesTheSessionAgain(t *testing.T) {
	write := permission.Request{
		CallID: "call-1",
		Tool:   "write",
		Action: permission.ActionWrite,
		Path:   "/w/a.go",
	}

	service := permission.New(prompterFunc(func(permission.Request) {}))
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})
	m.permissions = gated{service}
	if err := m.permissions.SetMode(permission.ModeManual); err != nil {
		t.Fatalf("putting the session in manual: %v", err)
	}
	if !service.Prompts(write) {
		t.Fatal("manual does not ask about a write, so nothing below is about a gate that was ever there")
	}

	m = typeCommand(m, "/yolo "+yoloConfirm)
	if service.Prompts(write) {
		t.Fatal("the service still asks about a write under the bypass")
	}

	m, _ = m.press(shiftTab)

	if m.status.mode != permission.ModeManual {
		t.Errorf("the press landed in %q; leaving the bypass lands in manual, not the next mode along",
			m.status.mode)
	}
	if m.status.yolo {
		t.Error("the badge outlived the press that left the bypass")
	}
	if !service.Prompts(write) {
		t.Error("the service still lets a write through, so the bypass outlived the mode it was left for")
	}
}

// TestArmingWithNothingToArmSaysSo, for the reason a mode switch with no service
// says so (mode.go): a command that quietly does nothing reads as one that
// worked, and here that mistake is the wrong way round about every guardrail.
func TestArmingWithNothingToArmSaysSo(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})

	m = typeCommand(m, "/yolo "+yoloConfirm)

	if m.status.yolo {
		t.Error("the badge went up with no service behind it")
	}
	if m.reminder != "" {
		t.Errorf("a session that armed nothing left %q for the next turn to tell the model", m.reminder)
	}
	if frame := words(m.View().Content); !strings.Contains(frame, "no permission service") {
		t.Errorf("the frame says nothing about the command that did nothing:\n%s", frame)
	}
}

// typeCommand types a slash command and sends it. Commands start no turn, so
// there is nothing to run and nothing to wait for (command.go).
func typeCommand(m Model, line string) Model {
	m, _ = typed(m, line).press(key(tea.KeyEnter))
	return m
}
