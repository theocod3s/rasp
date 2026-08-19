package tui_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs the leak detector over the package. The bridge is a goroutine
// held for the program's whole life, and one that outlives the program is a
// process that will not quit (design §13).
//
// The one exception is teatest's own: every TestModel starts a goroutine parked
// on a SIGINT it will never be sent, with no way to stop it. Ignored by the
// frame it is parked in rather than broadly, so the day teatest renames or drops
// it this stops matching and the suite says so.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/charmbracelet/x/exp/teatest/v2.NewTestModel.func2"))
}
