package tui_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs the leak detector over the package. The bridge is a goroutine
// held for the program's whole life, and one that outlives the program is a
// process that will not quit (design §13).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
