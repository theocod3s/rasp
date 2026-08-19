package tui_test

import (
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/tui"
)

// TestRunEndsThePumpWithTheProgram is the wiring, and TestMain checks the half
// this cannot see: Run owns the drainer's lifetime, so a UI the user quit leaves
// nothing behind. A pump still parked in Send is a process that will not exit.
func TestRunEndsThePumpWithTheProgram(t *testing.T) {
	const ctrlC = "\x03"

	p := tui.New(
		tea.WithInput(strings.NewReader(ctrlC)),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithoutSignalHandler(),
	)

	stopped := make(chan error, 1)
	go func() { stopped <- p.Run() }()

	p.Events(agent.Event{Kind: agent.EventAssistantEnd})
	p.Events(agent.Event{Kind: agent.EventTurnEnd})

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return; ctrl+c is the one key this skeleton binds, and it quits")
	}
}
