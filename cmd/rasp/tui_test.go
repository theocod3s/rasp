package main

import (
	"strings"
	"testing"
)

// TestTheRootStartsTheUIAndFailsBeforeItTakesTheTerminal runs the root command
// the way a user does, with a model id no adapter can serve. What it pins is the
// order: configuration is resolved and refused while the terminal is still the
// shell's, so a startup failure is a sentence on stderr rather than one written
// underneath a UI that already redrew the screen.
//
// It is also the only test that may run the root command. Given a configuration
// it accepts, this path opens the UI and reads stdin until the user quits.
func TestTheRootStartsTheUIAndFailsBeforeItTakesTheTerminal(t *testing.T) {
	projectConfig(t, `{"model": "claude-opus-5"}`)

	stdout, _, err := run(t)
	if err == nil {
		t.Fatal("`rasp` started with a model id that names no provider")
	}
	if !strings.Contains(err.Error(), "provider/id") {
		t.Errorf("error %q does not say what the model id should have looked like", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q; the screen belongs to the UI and this run never opened one", stdout)
	}
}

// TestTheRootRefusesAnArgument: a prompt goes into the UI, so a word after
// `rasp` is a subcommand that does not exist — and opening a session while
// ignoring what the user typed is the wrong answer to that. Cobra refuses it on
// the root's behalf, which is why the root declares no Args of its own.
func TestTheRootRefusesAnArgument(t *testing.T) {
	projectConfig(t, `{"model": "anthropic/claude-opus-5"}`)

	_, _, err := run(t, "explain", "this")
	if err == nil {
		t.Fatal("`rasp explain this` was accepted")
	}
	// Naming the word, not merely failing: a root that took the argument and
	// opened a session would fail here too, on the first thing the UI could not
	// do in a test — and pass a check that only asked whether something went
	// wrong.
	if !strings.Contains(err.Error(), `"explain"`) {
		t.Errorf("`rasp explain this` failed with %v, want the argument named", err)
	}
}
