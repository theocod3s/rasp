package tui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/llm/fake"
	"github.com/theocod3s/rasp/internal/tool"
)

// TestStreamingIntoARunningProgramMutatesOnlyInUpdate runs a real turn into a
// real Bubble Tea program, which is the only way to assert the rule: the turn's
// goroutine is writing the accumulated message for the whole stream while
// Update stores it and View reads it, and under -race any state reached from
// both ends says so.
//
// Streamed in many small chunks on purpose. The window the provider leaves
// between rewriting the message and the UI reading it is one delta wide, so a
// stream of a few chunks can pass a bridge that hands the live pointer straight
// through.
func TestStreamingIntoARunningProgramMutatesOnlyInUpdate(t *testing.T) {
	chunks := make([]string, 400)
	for i := range chunks {
		chunks[i] = "word "
	}
	want := strings.Repeat("word ", len(chunks))

	provider := fake.New(
		fake.Text(chunks...),
		fake.Done(llm.StopEndTurn),
	)

	// The filter runs on the rendering goroutine, before Update, and does the two
	// jobs a test outside that goroutine cannot.
	//
	// It reads the reply as it accumulates, which is where a bridge handing on
	// the provider's own message races the turn — and it records that a delta
	// reached the UI at all, without which the streaming half of this test is
	// unexercised and its silence reads as a pass.
	//
	// And it says when the turn's end has arrived: quitting the moment Send
	// returns would race the pump, which may still hold events. A Quit sent after
	// the filter sees the end is the next message this goroutine takes.
	var (
		ended    = make(chan struct{})
		streamed int
	)
	watch := func(model tea.Model, msg tea.Msg) tea.Msg {
		if root, ok := model.(Model); ok && root.streaming != nil {
			streamed = max(streamed, len(spoken(*root.streaming)))
		}
		if ev, ok := msg.(agentMsg); ok && ev.event.Kind == agent.EventTurnEnd {
			close(ended)
		}
		return msg
	}

	b := newBridge()
	prog := tea.NewProgram(Model{}, append(headless(), tea.WithFilter(watch))...)
	b.start(prog)

	type outcome struct {
		model tea.Model
		err   error
	}
	finished := make(chan outcome, 1)
	go func() {
		model, err := prog.Run()
		finished <- outcome{model, err}
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
	if err := ag.Send(context.Background(), "say something long"); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}

	select {
	case <-ended:
	case <-time.After(settle):
		t.Fatal("the turn ended and the UI never heard about it")
	}

	prog.Quit()
	var done outcome
	select {
	case done = <-finished:
	case <-time.After(settle):
		t.Fatal("the program did not return after Quit")
	}
	b.stop()

	if done.err != nil {
		t.Fatalf("the program returned %v", done.err)
	}
	final, ok := done.model.(Model)
	if !ok {
		t.Fatalf("the program returned a %T rather than the root model", done.model)
	}

	if streamed == 0 {
		t.Error("no delta ever reached the UI, so the turn's whole reply arrived at once and " +
			"nothing here was streamed")
	}

	// Without this the test passes on a bridge that delivers nothing at all,
	// which is the one outcome that is trivially race-free.
	if len(final.messages) != 1 {
		t.Fatalf("the UI holds %d finished reply(s) and the turn produced 1", len(final.messages))
	}
	if got := spoken(final.messages[0]); got != want {
		t.Fatalf("the UI holds a reply of %d characters and the model streamed %d", len(got), len(want))
	}
	if final.busy {
		t.Error("the UI is still busy after the turn ended")
	}
}

// headless is a program with no terminal attached. The renderer is off, which
// stops it writing escape sequences at a test's output; View is still called
// after every Update, which is what puts the model's state on the rendering
// goroutine.
func headless() []tea.ProgramOption {
	return []tea.ProgramOption{
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithoutSignalHandler(),
	}
}
