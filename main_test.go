package rasp_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs the leak detector over the package. The integration test drives
// the real loop, which spawns a goroutine per tool call and hands bash's output
// to one os/exec owns; a goroutine outliving its turn is a hung process on quit
// (design §13).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
