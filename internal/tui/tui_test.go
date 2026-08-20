package tui_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/tui"
)

const (
	ctrlC = "\x03"
	enter = "\r"

	// settle is how long a test waits for something the UI has already been told
	// to do. Reaching it is a failure, never a slow machine.
	settle = 5 * time.Second

	// beforehand is how long a test holds still to build confidence that
	// something has *not* happened yet. Too short and a slow machine passes the
	// check by accident; there is no way to make waiting for an absence certain,
	// only more or less likely to catch a real ordering bug.
	beforehand = 200 * time.Millisecond
)

// TestRunEndsThePumpWithTheProgram is the wiring, and TestMain checks the half
// this cannot see: Run owns the drainer's lifetime, so a UI the user quit leaves
// nothing behind. A pump still parked in Send is a process that will not exit.
func TestRunEndsThePumpWithTheProgram(t *testing.T) {
	p := tui.New(tui.Config{}, headless(typing(ctrlC))...)

	stopped := make(chan error, 1)
	go func() { stopped <- p.Run(idleTurner{}) }()

	p.Events(agent.Event{Kind: agent.EventAssistantEnd})
	p.Events(agent.Event{Kind: agent.EventTurnEnd})

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(settle):
		t.Fatal("Run did not return; ctrl+c is the one key this skeleton binds, and it quits")
	}
}

// TestTheUIKeepsHandlingKeysWhileATurnRuns is the acceptance criterion read the
// way a user meets it: a turn that has not finished must not cost the user their
// keyboard. The interrupt is what proves it — a Send called from Update would
// still be waiting for the cancellation only Update can deliver, and this test
// would reach its timeout with the program never having quit.
func TestTheUIKeepsHandlingKeysWhileATurnRuns(t *testing.T) {
	turner := newWaitingTurner()
	p := tui.New(tui.Config{}, headless(typing("hi"+enter+ctrlC))...)

	stopped := make(chan error, 1)
	go func() { stopped <- p.Run(turner) }()

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(settle):
		t.Fatal("the program never quit, so ctrl+c was never handled — Update is blocked on the turn")
	}

	// And the turn was a real one, cancelled: a prompt that never reached Send at
	// all would leave every assertion above holding.
	select {
	case err := <-turner.ran:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the turn's context ended with %v, want the cancellation the interrupt caused", err)
		}
	case <-time.After(settle):
		t.Fatal("no turn ever started, so nothing here was interrupted")
	}
}

// TestAProgramStoppedWithoutUpdateStillEndsItsTurn covers the interrupt no
// keybinding can reach. Bubble Tea answers a signal, or a cancelled program
// context, by ending the event loop: Update is never called, so the model is
// never offered the key that would cancel. A turn started before that would
// otherwise run on against a UI that has gone.
func TestAProgramStoppedWithoutUpdateStillEndsItsTurn(t *testing.T) {
	ctx, kill := context.WithCancel(t.Context())
	defer kill()

	// A pipe rather than a string: the reader has to stay open, or the program
	// ends at EOF and the cancellation below proves nothing.
	keys, keyboard := io.Pipe()
	defer keyboard.Close()

	turner := newWaitingTurner()
	p := tui.New(tui.Config{}, headless(tea.WithInput(keys), tea.WithContext(ctx))...)

	stopped := make(chan error, 1)
	go func() { stopped <- p.Run(turner) }()

	if _, err := io.WriteString(keyboard, "hi"+enter); err != nil {
		t.Fatalf("typing into the program: %v", err)
	}
	select {
	case <-turner.entered:
	case <-time.After(settle):
		t.Fatal("no turn ever started, so nothing here is being killed mid-turn")
	}

	kill()

	select {
	case <-stopped:
	case <-time.After(settle):
		t.Fatal("Run did not return after the program's context was cancelled")
	}
	select {
	case err := <-turner.ran:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the turn's context ended with %v, want the cancellation the program's exit caused", err)
		}
	case <-time.After(settle):
		t.Fatal("the turn outlived the program that started it")
	}
}

// TestRunWaitsForTheInFlightTurnBeforeReturning is the ordering session
// persistence will need: Run committing what a cancelled turn produced (design
// §4, decisions.md) is only real if nothing that reads the transcript after
// Run returns can race the commit. Bubble Tea does not give this for free —
// the goroutine running a tea.Cmd is deliberately not waited on at shutdown,
// so ctrl+c's own key handling has to hold Run open until Send itself returns.
func TestRunWaitsForTheInFlightTurnBeforeReturning(t *testing.T) {
	turner := newWaitingTurner()
	turner.release = make(chan struct{})
	p := tui.New(tui.Config{}, headless(typing("hi"+enter+ctrlC))...)

	stopped := make(chan error, 1)
	go func() { stopped <- p.Run(turner) }()

	select {
	case <-turner.entered:
	case <-time.After(settle):
		t.Fatal("no turn ever started, so nothing here is being cancelled mid-turn")
	}

	select {
	case <-stopped:
		t.Fatal("Run returned while the turn's Send call was still running")
	case <-time.After(beforehand):
	}

	close(turner.release)

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(settle):
		t.Fatal("Run never returned once the turn's Send call did")
	}
}

// headless is a program with no terminal at either end.
func headless(opts ...tea.ProgramOption) []tea.ProgramOption {
	return append([]tea.ProgramOption{
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithoutSignalHandler(),
	}, opts...)
}

func typing(keys string) tea.ProgramOption { return tea.WithInput(strings.NewReader(keys)) }

// idleTurner is for the tests that never send a prompt.
type idleTurner struct{}

func (idleTurner) Send(context.Context, string) error { return nil }

// waitingTurner stays in Send until its turn is cancelled, and reports how the
// context ended. release is nil for the ordinary case; a test that needs Send
// held open past the cancellation itself sets it, which is what turns "Run and
// the turn both eventually finish" into "Run does not finish before the turn
// does" — waiting for each separately would still pass if Run returned first.
type waitingTurner struct {
	entered chan struct{}
	ran     chan error
	release chan struct{}
}

func newWaitingTurner() *waitingTurner {
	return &waitingTurner{entered: make(chan struct{}), ran: make(chan error, 1)}
}

func (w *waitingTurner) Send(ctx context.Context, _ string) error {
	close(w.entered)
	<-ctx.Done()
	if w.release != nil {
		<-w.release
	}
	w.ran <- ctx.Err()
	return agent.ErrInterrupted
}
