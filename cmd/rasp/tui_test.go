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

	// With --yolo, which the root reads before it builds anything: a flag renamed
	// out from under that read fails here, naming itself instead of the model.
	stdout, _, err := run(t, "--"+yoloFlag)
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

// TestTheBypassFlagBelongsToTheSessionAndNowhereElse. It arms a session with a
// user watching it, and a subcommand that accepted the word and ignored it would
// be a flag reading as applied when it is not — worst of all on `config check`,
// whose whole job is saying what is in force.
func TestTheBypassFlagBelongsToTheSessionAndNowhereElse(t *testing.T) {
	if newRootCmd().Flags().Lookup(yoloFlag) == nil {
		t.Fatalf("the root has no --%s, so there is no way to start a session with one", yoloFlag)
	}

	projectConfig(t, `{"model": "anthropic/claude-opus-5"}`)
	for _, args := range [][]string{
		{"config", "check", "--" + yoloFlag},
		{"run", "-p", "hello", "--" + yoloFlag},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, _, err := run(t, args...)
			if err == nil {
				t.Fatalf("`rasp %s` was accepted", strings.Join(args, " "))
			}
			if !strings.Contains(err.Error(), yoloFlag) {
				t.Errorf("it failed with %v, and the flag it could not take is not named", err)
			}
		})
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
