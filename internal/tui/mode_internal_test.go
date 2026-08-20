package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/fake"
	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tool"
)

// shiftTab is the key that cycles the mode. Terminals spell it `CSI Z`, which
// Bubble Tea decodes to Tab carrying Shift.
var shiftTab = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}

// TestShiftTabCyclesPlanManualAuto walks the whole rotation and back to where it
// started, through the key rather than through nextMode: a cycle that is right
// and bound to nothing is a cycle the user cannot reach.
func TestShiftTabCyclesPlanManualAuto(t *testing.T) {
	service := &answers{}
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModePlan})
	m.permissions = service

	want := []permission.Mode{permission.ModeManual, permission.ModeAuto, permission.ModePlan}
	for i, mode := range want {
		m = update(m, shiftTab)
		if m.status.mode != mode {
			t.Fatalf("press %d put the status line in %q, want %q", i+1, m.status.mode, mode)
		}
	}

	// The service is the half that can go missing without the screen saying so.
	if len(service.modes) != len(want) {
		t.Fatalf("the service was put in %v; every press has to reach it", service.modes)
	}
	for i, mode := range want {
		if service.modes[i] != mode {
			t.Errorf("press %d put the service in %q, want %q", i+1, service.modes[i], mode)
		}
	}
}

// TestNoAmountOfCyclingReachesYolo is the guarantee design §7.1 asks the cycle
// array to carry rather than merely document: yolo is reached by naming it, and
// leaning on the key is not naming it.
//
// Asserted against the presets rather than against a list written here, so a
// mode added to the cycle with no rules behind it fails this too — the status
// line would say a name the ladder cannot run under.
func TestNoAmountOfCyclingReachesYolo(t *testing.T) {
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
	presets := permission.Presets()
	for i, mode := range service.modes {
		if mode == "yolo" {
			t.Fatalf("press %d reached yolo, which no number of presses may", i+1)
		}
		if _, ok := presets[mode]; !ok {
			t.Errorf("press %d reached %q, which has no permission rules to run under", i+1, mode)
		}
	}
}

// TestEachModeInTheCycleNamesItselfToTheModel. The reminder is the model's only
// notice that its constraints moved, so one that names the wrong mode — or the
// same mode for two of them — teaches it the opposite of what happened.
func TestEachModeInTheCycleNamesItselfToTheModel(t *testing.T) {
	seen := make(map[string]permission.Mode, len(cycleModes))
	for _, mode := range cycleModes {
		reminder := modeReminder(mode)
		if !strings.Contains(reminder, string(mode)) {
			t.Errorf("the reminder for %q does not name it: %q", mode, reminder)
		}
		if first, ok := seen[reminder]; ok {
			t.Errorf("%q and %q are told the same thing", first, mode)
		}
		seen[reminder] = mode
	}
}

// TestASwitchRidesInOnTheNextTurn is design §7.5 at the seam it crosses. The
// transcript reaches the provider only when a turn does, so the sentence waits
// here — and it has to arrive ahead of the work done under the new mode, not
// after it.
func TestASwitchRidesInOnTheNextTurn(t *testing.T) {
	const typed = "carry on"

	turner := &recordingTurner{}
	m := newModel(t.Context(), turner, Config{Mode: permission.ModeManual})
	m.permissions = &answers{}

	m = update(m, shiftTab) // manual → auto
	m = sendLine(t, m, typed)

	if len(turner.sent) != 1 {
		t.Fatalf("the turner was sent %v, want the one turn", turner.sent)
	}
	sent := turner.sent[0]
	reminder := modeReminder(permission.ModeAuto)
	if !strings.HasPrefix(sent, reminder) {
		t.Errorf("the turn carried %q; it has to open with %q", sent, reminder)
	}
	if !strings.HasSuffix(sent, typed) {
		t.Errorf("the turn carried %q, and the user's own message is not the end of it", sent)
	}

	// On the screen it is a notice, not a user bubble: the user's line stands as
	// they typed it, with the switch drawn above it (design §7.5).
	frame := words(m.View().Content)
	if !strings.Contains(frame, words(reminder)) {
		t.Errorf("the switch was never drawn:\n%s", frame)
	}
	if strings.Contains(frame, words(reminder)+" "+typed) {
		t.Errorf("the reminder was drawn as part of the user's own message:\n%s", frame)
	}

	// And it is said once. A reminder repeated every turn is a mode change the
	// model is told about long after it happened.
	m = update(m, turnDone{})
	m = sendLine(t, m, "and again")

	if len(turner.sent) != 2 {
		t.Fatalf("the turner was sent %v, want the second turn", turner.sent)
	}
	if strings.Contains(turner.sent[1], "Mode changed") {
		t.Errorf("the second turn carried the switch again: %q", turner.sent[1])
	}
}

// TestOnlyTheLastSwitchIsCarried. Cycling twice to read the names is an ordinary
// thing to do, and a turn opening with two mode changes tells the model
// something contradictory about the one it is in.
func TestOnlyTheLastSwitchIsCarried(t *testing.T) {
	turner := &recordingTurner{}
	m := newModel(t.Context(), turner, Config{Mode: permission.ModeManual})
	m.permissions = &answers{}

	m = update(m, shiftTab) // manual → auto
	m = update(m, shiftTab) // auto → plan
	m = sendLine(t, m, "go")

	if len(turner.sent) != 1 {
		t.Fatalf("the turner was sent %v, want the one turn", turner.sent)
	}
	if n := strings.Count(turner.sent[0], "Mode changed"); n != 1 {
		t.Fatalf("the turn opens with %d mode changes: %q", n, turner.sent[0])
	}
	if !strings.HasPrefix(turner.sent[0], modeReminder(permission.ModePlan)) {
		t.Errorf("the turn carried %q, want the mode the session ended up in", turner.sent[0])
	}
}

// TestTheStatusLineSaysTheNewModeWhileATurnRuns. The mode indicator is what
// makes a permissive mode safe to have, and a turn in flight is exactly when a
// user reaches for the key — so the frame after the keystroke has to say the new
// mode whatever the turn is doing.
func TestTheStatusLineSaysTheNewModeWhileATurnRuns(t *testing.T) {
	turner := newTurner(agent.ErrInterrupted)
	m := newModel(t.Context(), turner, Config{Mode: permission.ModeManual})
	m.permissions = &answers{}

	m, cmd := typed(m, "go").press(key(tea.KeyEnter))
	run(cmd)
	started(t, turner.started)
	if !m.busy {
		t.Fatal("no turn is running, so this says nothing about a switch made during one")
	}

	m, _ = m.press(shiftTab)

	if frame := words(m.View().Content); !strings.Contains(frame, string(permission.ModeAuto)) {
		t.Errorf("the frame after the keystroke does not say auto:\n%s", frame)
	}
	m.interrupt()
}

// TestASwitchWhileAQuestionStandsLeavesItStanding is the deliberate answer to
// what the key does with the keyboard already spoken for.
//
// It passes through: the question takes the input line, not the session, as Esc
// and ctrl+c already do. And it leaves the question alone — that call was
// resolved to "ask" under the mode it was asked in, and re-deciding it under the
// new one is the retroactivity design §7.4 rules out. The user's y still
// approves the call they were asked about; the next call meets the new mode.
func TestASwitchWhileAQuestionStandsLeavesItStanding(t *testing.T) {
	service := &answers{decided: true}
	m, clock := asked(t, service, editRequest())
	clock.pass(promptGrace)

	m, _ = m.press(shiftTab)

	if m.status.mode != permission.ModeAuto {
		t.Errorf("the status line says %q; the switch was swallowed by the open question", m.status.mode)
	}
	if len(service.modes) != 1 || service.modes[0] != permission.ModeAuto {
		t.Errorf("the service was put in %v, want auto", service.modes)
	}
	if len(service.given) != 0 {
		t.Errorf("the switch answered the question with %v", service.given)
	}
	if !m.asking() {
		t.Fatal("the switch closed the question, leaving the turn behind it waiting on an answer nobody can give")
	}
	if frame := words(m.View().Content); !strings.Contains(frame, "edit needs your approval") {
		t.Errorf("the question is gone from the frame:\n%s", frame)
	}

	// And the three keys still mean what they meant.
	m, _ = m.press(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if len(service.given) != 1 || service.given[0].decision != permission.DecisionOnce {
		t.Errorf("the service was told %v after the switch, want the question approved once", service.given)
	}
}

// TestASwitchWithNothingToSwitchSaysSo. A key that quietly does nothing reads as
// a terminal that ate it, and the user leans on it — which is worse here than
// elsewhere, because what they are leaning on is the guardrail.
func TestASwitchWithNothingToSwitchSaysSo(t *testing.T) {
	m := newModel(t.Context(), &promptTurner{}, Config{Mode: permission.ModeManual})

	m, _ = m.press(shiftTab)

	if m.status.mode != permission.ModeManual {
		t.Errorf("the status line moved to %q with no service to put in it", m.status.mode)
	}
	if m.reminder != "" {
		t.Errorf("a switch that did not happen left %q for the next turn to tell the model", m.reminder)
	}
	if frame := words(m.View().Content); !strings.Contains(frame, "no permission service") {
		t.Errorf("the frame says nothing about the key that did nothing:\n%s", frame)
	}
}

// TestAMidTurnSwitchMeetsTheNextCallAndNotTheRunningOne is the third criterion
// end to end, with nothing faked below the UI: the real loop, the real ladder,
// the real presets, and a keystroke landing while a tool call is inside its Run.
//
// The turn starts in auto, where a write is allowed outright. Shift+Tab lands
// while the first write is running and puts the session in plan, where a write
// is denied. What has to hold is both halves of design §7.4: the call already
// running finishes and succeeds, and the next one is refused.
func TestAMidTurnSwitchMeetsTheNextCallAndNotTheRunningOne(t *testing.T) {
	hold := &holdingTool{entered: make(chan struct{}), release: make(chan struct{})}
	provider := fake.New(
		fake.Text("Writing the first one."),
		fake.ToolCall(hold.Name(), `{"path":"a.go"}`),
		fake.Done(llm.StopToolUse),

		fake.Text("And the second."),
		fake.ToolCall(hold.Name(), `{"path":"b.go"}`),
		fake.Done(llm.StopToolUse),

		fake.Text("The second one was refused."),
		fake.Done(llm.StopEndTurn),
	)

	// A prompt is a failure here, not a step: neither mode this test passes
	// through asks about a write, so a question means the ladder answered at a
	// rung the test is not about. Rejected rather than left hanging, so the turn
	// ends and the count below is what reports it.
	asked := make(chan permission.Request, 4)
	var service *permission.Service
	service = permission.New(prompterFunc(func(req permission.Request) {
		asked <- req
		service.Resolve(req.CallID, permission.DecisionReject)
	}))

	a, err := agent.New(agent.Config{
		Provider:  provider,
		Tools:     tool.NewRegistry([]tool.Tool{hold}),
		Model:     "fake-model",
		MaxTokens: 1024,
		Approver:  writeGate{service},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}

	m := newModel(t.Context(), a, Config{Mode: permission.ModeAuto})
	m.permissions = gated{service}
	// The mode the session starts in, installed the way the composition root
	// installs it (cmd/rasp/permission.go) rather than through the key — a
	// startup is not a switch, and telling the model its mode changed before it
	// has said anything would be a lie.
	if err := m.permissions.SetMode(permission.ModeAuto); err != nil {
		t.Fatalf("putting the session in auto: %v", err)
	}

	m, cmd := typed(m, "write a.go and b.go").press(key(tea.KeyEnter))
	done := run(cmd)

	select {
	case <-hold.entered:
	case <-time.After(settle):
		t.Fatal("the turn never reached the tool, so there is no running call to switch under")
	}

	m, _ = m.press(shiftTab) // auto → plan

	if m.status.mode != permission.ModePlan {
		t.Fatalf("the status line says %q; the rest of this test rests on the session being in plan",
			m.status.mode)
	}
	if frame := words(m.View().Content); !strings.Contains(frame, string(permission.ModePlan)) {
		t.Errorf("the frame drawn while the call was still running does not say plan:\n%s", frame)
	}

	close(hold.release)
	if outcome := waitFor(t, done); outcome.err != nil {
		t.Fatalf("the turn ended with %v; a refused call is a result the loop carries on from", outcome.err)
	}

	if n := hold.count(); n != 1 {
		t.Errorf("the tool ran %d time(s); the first call runs and the second never gets that far", n)
	}
	if len(asked) != 0 {
		t.Errorf("the ladder put %d question(s) to the user; auto allows a write and plan denies one",
			len(asked))
	}

	results := toolResults(a.Messages())
	if len(results) != 2 {
		t.Fatalf("the transcript answers %d tool call(s), want the one that ran and the one that was "+
			"refused: %+v", len(results), results)
	}
	if results[0].IsError {
		t.Errorf("the call already running when the mode changed came back as %q; it was approved "+
			"under auto and finishes under auto (design §7.4)", results[0].Content)
	}
	for _, want := range []string{"was not run", permission.ErrDenied.Error()} {
		if !strings.Contains(results[1].Content, want) {
			t.Errorf("the call after the switch came back as %q, and nothing in it says %q",
				results[1].Content, want)
		}
	}
}

// toolResults is every tool_result in a transcript, in the order the calls were
// answered.
func toolResults(msgs []llm.Message) []llm.Block {
	var out []llm.Block
	for _, msg := range msgs {
		for _, block := range msg.Content {
			if block.Type == llm.BlockToolResult {
				out = append(out, block)
			}
		}
	}
	return out
}

// sendLine types a line and presses Enter, then runs the turn Enter handed back
// and settles its outcome — so the next assertion is about a turn that finished
// rather than one still starting.
func sendLine(t *testing.T, m Model, text string) Model {
	t.Helper()

	m, cmd := typed(m, text).press(key(tea.KeyEnter))
	if cmd == nil {
		t.Fatalf("Enter started no turn for %q", text)
	}
	return update(m, waitFor(t, run(cmd)))
}

// gated is what the composition root does, in miniature (cmd/rasp/permission.go):
// a real service, put into a mode by compiling that mode's preset.
type gated struct{ service *permission.Service }

func (g gated) Resolve(callID string, d permission.Decision) bool {
	return g.service.Resolve(callID, d)
}

func (g gated) SetYolo(on bool) { g.service.SetYolo(on) }

func (g gated) SetMode(mode permission.Mode) error {
	preset, ok := permission.Presets()[mode]
	if !ok {
		return fmt.Errorf("mode %q has no permission rules", mode)
	}
	rules, err := permission.Compile(preset)
	if err != nil {
		return err
	}
	g.service.SetRules(rules)
	return nil
}

// recordingTurner keeps what each turn was actually sent, which is the only
// place the reminder is visible as the model will read it.
type recordingTurner struct{ sent []string }

func (r *recordingTurner) Send(_ context.Context, text string) error {
	r.sent = append(r.sent, text)
	return nil
}

// holdingTool stays inside Run until the test releases it, which is what gives a
// mid-turn keystroke somewhere to land.
type holdingTool struct {
	entered chan struct{}
	release chan struct{}

	mu   sync.Mutex
	runs int
}

func (h *holdingTool) Name() string           { return "write" }
func (h *holdingTool) Description() string    { return "holds the turn open" }
func (h *holdingTool) Schema() map[string]any { return map[string]any{"type": "object"} }

func (h *holdingTool) Run(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
	h.mu.Lock()
	h.runs++
	first := h.runs == 1
	h.mu.Unlock()

	if !first {
		return tool.Result{Content: "wrote it"}, nil
	}
	close(h.entered)
	select {
	case <-h.release:
		return tool.Result{Content: "wrote it"}, nil
	case <-ctx.Done():
		return tool.Result{}, errors.New("the turn was cancelled before the tool was released")
	}
}

func (h *holdingTool) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runs
}
