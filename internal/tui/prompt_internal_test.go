package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/fake"
	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tool"
)

// TestAnOpenQuestionTakesTheKeyboardOffTheInputLine. The turn is blocked on the
// answer, so a line typed now would be composed for a prompt nothing can send —
// and Enter, which normally starts a turn, has no turn to start.
func TestAnOpenQuestionTakesTheKeyboardOffTheInputLine(t *testing.T) {
	answers := &answers{}
	m, clock := asked(t, answers, editRequest())
	clock.pass(promptGrace)

	for _, press := range []tea.KeyPressMsg{
		{Code: 'h', Text: "h"},
		{Code: 'i', Text: "i"},
		{Code: tea.KeyEnter},
		{Code: tea.KeyBackspace},
	} {
		m, _ = m.press(press)
	}

	if m.input != "" {
		t.Errorf("the input line holds %q; a key pressed against an open question is the answer to "+
			"it, not a message", m.input)
	}
	if m.busy {
		t.Error("Enter started a turn while a question was standing")
	}
	if !m.asking() {
		t.Error("the question is gone and none of those keys answered it")
	}
	if len(answers.given) != 0 {
		t.Errorf("the service was told %v; none of those keys is an answer", answers.given)
	}
	if frame := words(m.View().Content); !strings.Contains(frame, "edit needs your approval") {
		t.Errorf("the question is not in the frame:\n%s", frame)
	}
}

// TestTheThreeAnswersReachTheService is the once/always/reject criterion at the
// seam it has to cross: what the key means is decided here and meant by
// permission, so a key wired to the wrong decision hands out a session-long
// grant for a press that meant "just this once".
func TestTheThreeAnswersReachTheService(t *testing.T) {
	tests := []struct {
		key  rune
		want permission.Decision
	}{
		{'y', permission.DecisionOnce},
		{'a', permission.DecisionAlways},
		{'n', permission.DecisionReject},
	}
	for _, tc := range tests {
		t.Run(string(tc.key), func(t *testing.T) {
			answers := &answers{decided: true}
			m, clock := asked(t, answers, editRequest())
			clock.pass(promptGrace)

			m, _ = m.press(tea.KeyPressMsg{Code: tc.key})

			if len(answers.given) != 1 {
				t.Fatalf("the service was told %v, want one answer", answers.given)
			}
			if got := answers.given[0]; got.callID != editRequest().CallID || got.decision != tc.want {
				t.Errorf("%q answered %+v, want %q for call %q",
					tc.key, got, tc.want, editRequest().CallID)
			}
			if m.asking() {
				t.Error("the question is still open after it was answered")
			}
			if frame := words(m.View().Content); strings.Contains(frame, "needs your approval") {
				t.Errorf("the answered question is still on the screen:\n%s", frame)
			}

			// And the keyboard goes back to composing, or the session is over.
			m, _ = m.press(tea.KeyPressMsg{Code: 'h', Text: "h"})
			if m.input != "h" {
				t.Errorf("the input line holds %q after the question was answered", m.input)
			}
		})
	}
}

// TestAKeystrokeInsideTheGracePeriodIsAbsorbed. A question appears while the
// user may be mid-word, and its three keys are ordinary letters: without the
// pause the `a` of a sentence already being typed is a grant for the session.
func TestAKeystrokeInsideTheGracePeriodIsAbsorbed(t *testing.T) {
	answers := &answers{decided: true}
	m, clock := asked(t, answers, editRequest())

	clock.pass(promptGrace - time.Nanosecond)
	m, _ = m.press(tea.KeyPressMsg{Code: 'a', Text: "a"})

	if len(answers.given) != 0 {
		t.Fatalf("a key %s after the question opened answered it with %v", promptGrace, answers.given)
	}
	if !m.asking() {
		t.Fatal("the swallowed key closed the question anyway")
	}
	if m.input != "" {
		t.Errorf("the swallowed key was composed into %q instead", m.input)
	}

	clock.pass(time.Nanosecond)
	m, _ = m.press(tea.KeyPressMsg{Code: 'a', Text: "a"})

	if len(answers.given) != 1 {
		t.Errorf("the service was told %v once the grace period had passed, want the one answer",
			answers.given)
	}
}

// TestAModifiedKeyIsNotAnAnswer. Ctrl+a is a reflex on any line editor, and the
// grace period does not cover a reflex the user has every intention of pressing.
func TestAModifiedKeyIsNotAnAnswer(t *testing.T) {
	answers := &answers{decided: true}
	m, clock := asked(t, answers, editRequest())
	clock.pass(promptGrace)

	m, _ = m.press(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})

	if len(answers.given) != 0 {
		t.Errorf("ctrl+a answered the question with %v", answers.given)
	}
	if !m.asking() {
		t.Error("ctrl+a closed the question")
	}
}

// TestATurnThatEndedTakesItsQuestionWithIt is the cancellation criterion. The
// Ask behind a question returns the moment its context does, and the service
// forgets the call — so a question left on the screen points the keyboard at a
// turn that has gone, and the answer it takes lands nowhere.
//
// Both routes out of a turn, because which arrives first is not promised: the
// loop's own EventTurnEnd, and the turnDone that says what Send returned.
func TestATurnThatEndedTakesItsQuestionWithIt(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.Msg
	}{
		{"the loop's turn end", agentMsg{event: agent.Event{Kind: agent.EventTurnEnd}}},
		{"what Send returned", turnDone{err: agent.ErrInterrupted}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			answers := &answers{decided: true}
			m, clock := asked(t, answers, editRequest())
			clock.pass(promptGrace)

			m = update(m, tc.msg)

			if m.asking() {
				t.Fatal("the question outlived the turn that asked it")
			}
			if len(answers.given) != 0 {
				t.Errorf("the UI answered %v on its own; nobody pressed a key", answers.given)
			}
			if frame := words(m.View().Content); strings.Contains(frame, "needs your approval") {
				t.Errorf("the abandoned question is still on the screen:\n%s", frame)
			}

			m, _ = m.press(tea.KeyPressMsg{Code: 'y', Text: "y"})
			if m.input != "y" {
				t.Errorf("the input line holds %q; the keyboard is still pointed at the dead question",
					m.input)
			}
		})
	}
}

// TestAnAnswerTheServiceNoLongerWantsStillClosesTheQuestion. Resolve reports
// false for a call it no longer holds — one cancelled with its turn between the
// keypress and the delivery. The question is gone either way, and a UI that
// waited for a second answer would wait for the rest of the session.
func TestAnAnswerTheServiceNoLongerWantsStillClosesTheQuestion(t *testing.T) {
	answers := &answers{decided: false}
	m, clock := asked(t, answers, editRequest())
	clock.pass(promptGrace)

	m, _ = m.press(tea.KeyPressMsg{Code: 'y', Text: "y"})

	if m.asking() {
		t.Error("the question is still open after an answer the service had no use for")
	}
}

// TestAQuestionWithNothingToAnswerItSaysSo is the wiring failure this ticket
// exists to make impossible, caught at the one place it can still happen: a UI
// built with no permission service still receives the question, and the turn
// behind it is blocked whatever is drawn. Saying so beats drawing three keys
// that do nothing.
func TestAQuestionWithNothingToAnswerItSaysSo(t *testing.T) {
	m := update(newModel(t.Context(), &promptTurner{}, Config{}), promptMsg{request: editRequest()})

	if m.asking() {
		t.Fatal("a question is open with nothing able to answer it, so the keyboard is pointed at a dead end")
	}
	frame := words(m.View().Content)
	if !strings.Contains(frame, "nothing wired up to answer it") {
		t.Errorf("the frame says nothing about the question that cannot be answered:\n%s", frame)
	}

	// And the keyboard is still the user's, so the interrupt is reachable.
	m, _ = m.press(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if m.input != "y" {
		t.Errorf("the input line holds %q", m.input)
	}
}

// TestASecondQuestionWaitsForTheFirst. A batch puts its questions one at a time
// (agent/tools.go), so this is the shape that should not arise — and if it does,
// the second must not overwrite the first: the turn behind an overwritten
// question waits for an answer no key can deliver.
func TestASecondQuestionWaitsForTheFirst(t *testing.T) {
	second := permission.Request{CallID: "call_2", Tool: "bash", Action: permission.ActionExecute,
		Command: "go test ./..."}

	answers := &answers{decided: true}
	m, clock := asked(t, answers, editRequest())
	m = update(m, promptMsg{request: second})
	clock.pass(promptGrace)

	if frame := words(m.View().Content); strings.Contains(frame, "bash needs your approval") {
		t.Errorf("both questions are on the screen, and the same three keys answer only one:\n%s", frame)
	}

	m, _ = m.press(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if len(answers.given) != 1 || answers.given[0].callID != editRequest().CallID {
		t.Fatalf("the first key answered %v, want the question that was drawn", answers.given)
	}
	if !m.asking() {
		t.Fatal("the second question was dropped with the first")
	}
	if frame := words(m.View().Content); !strings.Contains(frame, "bash needs your approval") {
		t.Errorf("the second question was never drawn:\n%s", frame)
	}

	// Its own grace period: it appeared under the key that answered the one
	// before it, which is exactly the stray keystroke the pause is for.
	m, _ = m.press(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if len(answers.given) != 1 {
		t.Errorf("the question answered %v the instant it was drawn", answers.given)
	}
	clock.pass(promptGrace)
	m, _ = m.press(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if len(answers.given) != 2 || answers.given[1].callID != second.CallID {
		t.Errorf("the service was told %v, want the second question answered too", answers.given)
	}
}

// TestATurnStopsAtTheQuestionAndRunsOnTheAnswer is the whole path, with nothing
// faked below the UI: the real loop, the real ladder in its default mode, the
// real bridge carrying the question in, and a real tool on the far side of the
// gate. What it proves is what no unit test here can — that the turn is
// *blocked* by the question rather than merely told about it, and that the three
// answers mean different things to the run that follows.
func TestATurnStopsAtTheQuestionAndRunsOnTheAnswer(t *testing.T) {
	tests := []struct {
		name    string
		key     rune
		asked   int
		ran     int
		refused bool
	}{
		// Two identical calls, so a grant that covers the second is visible as
		// one question rather than two.
		{name: "once approves the call it was asked about", key: 'y', asked: 2, ran: 2},
		{name: "always covers the call that repeats it", key: 'a', asked: 1, ran: 2},
		{name: "reject runs nothing", key: 'n', asked: 2, ran: 0, refused: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			touch := &countingTool{ran: make(chan string, 4)}
			provider := fake.New(
				fake.Text("Writing it."),
				fake.ToolCall(touch.Name(), `{"path":"a.go"}`),
				fake.Done(llm.StopToolUse),

				fake.ToolCall(touch.Name(), `{"path":"a.go"}`),
				fake.Done(llm.StopToolUse),

				fake.Text("Done."),
				fake.Done(llm.StopEndTurn),
			)

			pump := &sink{msgs: make(chan tea.Msg, mailbox)}
			bridge := newBridge()
			bridge.start(pump)
			defer bridge.stop()

			service := permission.New(prompterFunc(bridge.prompt))
			rules, err := permission.Compile(permission.Presets()[permission.ModeManual])
			if err != nil {
				t.Fatalf("compiling the manual preset: %v", err)
			}
			service.SetRules(rules)

			a, err := agent.New(agent.Config{
				Provider:  provider,
				Tools:     tool.NewRegistry([]tool.Tool{touch}),
				Model:     "fake-model",
				MaxTokens: 1024,
				Approver:  writeGate{service},
				Events:    bridge.handle,
			})
			if err != nil {
				t.Fatalf("agent.New: %v", err)
			}

			clock := &clock{now: goldenNow}
			m := newModel(t.Context(), a, Config{Mode: permission.ModeManual})
			m.now = clock.read
			m.permissions = serviceAnswers{service}

			m, cmd := typed(m, "write a.go").press(key(tea.KeyEnter))
			done := run(cmd)

			asked := 0
			for finished := false; !finished; {
				select {
				case msg := <-pump.msgs:
					m = update(m, msg)
					if _, ok := msg.(promptMsg); !ok {
						continue
					}
					asked++
					if asked == 1 && len(touch.ran) != 0 {
						t.Fatal("the tool ran before the question was answered, so the gate is " +
							"telling the user about a call it already let through")
					}
					clock.pass(promptGrace)
					m, _ = m.press(tea.KeyPressMsg{Code: tc.key})
				case msg := <-done:
					m = update(m, msg)
					finished = true
				case <-time.After(settle):
					t.Fatalf("the turn neither asked nor finished; it asked %d time(s) so far", asked)
				}
			}

			if asked != tc.asked {
				t.Errorf("the turn asked %d time(s), want %d", asked, tc.asked)
			}
			if got := len(touch.ran); got != tc.ran {
				t.Errorf("the tool ran %d time(s), want %d", got, tc.ran)
			}
			if m.asking() {
				t.Error("a question is still on the screen after the turn ended")
			}
			if tc.refused && !strings.Contains(words(m.View().Content), "failed") {
				t.Errorf("nothing on the screen says the rejected call did not run:\n%s", m.View().Content)
			}
		})
	}
}

func editRequest() permission.Request {
	return permission.Request{
		CallID: "call_1",
		Tool:   "edit",
		Action: permission.ActionEdit,
		Path:   "/w/auth.go",
	}
}

// asked is a model with one question standing, drawn through Update so a case
// it does not route is a failure here rather than an untested method call.
func asked(t *testing.T, answers Permissions, req permission.Request) (Model, *clock) {
	t.Helper()

	c := &clock{now: goldenNow}
	m := newModel(t.Context(), &promptTurner{}, Config{})
	m.now = c.read
	m.permissions = answers

	m = update(m, promptMsg{request: req})
	if !m.asking() {
		t.Fatal("the model has no question open, so every assertion about answering one is vacuous")
	}
	// An item in the conversation, not a line of chrome: the question belongs
	// where it was asked, under the cards for the batch that asked it.
	if m.chat.Len() != 1 {
		t.Fatalf("the conversation holds %d item(s); the question is drawn as one of them", m.chat.Len())
	}
	return m, c
}

// clock is time a test moves by hand. Read only on the goroutine that drives
// Update, which is the only one that reads the model's own clock.
type clock struct{ now time.Time }

func (c *clock) read() time.Time      { return c.now }
func (c *clock) pass(d time.Duration) { c.now = c.now.Add(d) }

// answers is the permission service as the UI reaches it, recording what it was
// told. decided is what Resolve reports: false is a question the service no
// longer holds.
type answers struct {
	given   []given
	decided bool
	modes   []permission.Mode
	yolos   []bool
}

type given struct {
	callID   string
	decision permission.Decision
}

func (a *answers) Resolve(callID string, d permission.Decision) bool {
	a.given = append(a.given, given{callID: callID, decision: d})
	return a.decided
}

// SetMode records the switch and, as the real service does, ends any bypass the
// session had armed (permission/service.go).
func (a *answers) SetMode(mode permission.Mode) error {
	a.modes = append(a.modes, mode)
	a.yolos = append(a.yolos, false)
	return nil
}

func (a *answers) SetYolo(on bool) { a.yolos = append(a.yolos, on) }

// armed is whether the last thing this was told left the bypass on.
func (a *answers) armed() bool {
	return len(a.yolos) > 0 && a.yolos[len(a.yolos)-1]
}

// serviceAnswers is a real service standing in for the UI's whole seam. Its
// mode never changes, which is what a session that never presses the key does.
type serviceAnswers struct{ *permission.Service }

func (serviceAnswers) SetMode(permission.Mode) error { return nil }

type prompterFunc func(permission.Request)

func (f prompterFunc) Prompt(req permission.Request) { f(req) }

// writeGate is the mapping the composition root owns: the loop speaks tool
// calls and the ladder speaks requests, and neither knows the other's
// vocabulary (decisions.md). Here every call is the write tool, keyed on the
// path it names so a grant covers that file and not the next one.
type writeGate struct{ service *permission.Service }

func (g writeGate) Prompts(call llm.ToolCall) bool { return g.service.Prompts(g.request(call)) }

func (g writeGate) Approve(ctx context.Context, call llm.ToolCall) error {
	return g.service.Ask(ctx, g.request(call))
}

func (g writeGate) request(call llm.ToolCall) permission.Request {
	var args struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(call.Input, &args)
	return permission.Request{
		CallID: call.ID,
		Tool:   call.Name,
		Action: permission.ActionWrite,
		Path:   args.Path,
	}
}

// sink is the pump's far end, holding what it delivered until the test takes it.
type sink struct{ msgs chan tea.Msg }

func (s *sink) Send(msg tea.Msg) { s.msgs <- msg }

// countingTool records the calls that reach it, which is the far side of the
// gate: a call here is one the user approved.
type countingTool struct{ ran chan string }

func (c *countingTool) Name() string           { return "write" }
func (c *countingTool) Description() string    { return "records that it ran" }
func (c *countingTool) Schema() map[string]any { return map[string]any{"type": "object"} }

func (c *countingTool) Run(_ context.Context, input json.RawMessage) (tool.Result, error) {
	c.ran <- string(input)
	return tool.Result{Content: "wrote it"}, nil
}
