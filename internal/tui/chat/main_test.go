package chat_test

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// Chroma's regex engine runs one shared clock goroutine while a code block is
	// being highlighted and for a second after, then stops it itself. Nobody here
	// owns it, and waiting it out would add that second to every run.
	goleak.VerifyTestMain(m, goleak.IgnoreAnyFunction("github.com/dlclark/regexp2.runClock"))
}
