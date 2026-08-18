package builtin

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs the leak detector over the package. bash hands its output to a
// goroutine os/exec owns, and WaitDelay can return from Wait while that
// goroutine is still finishing, which is exactly the shape that leaks.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
