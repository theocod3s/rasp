package rasp_test

import "testing"

// Deliberately racy: two goroutines write x with no synchronisation. This passes
// under plain `go test` and only fails under `-race`, which is the point — it
// proves the race step catches something the test step cannot.
func TestBreakRace(t *testing.T) {
	x := 0
	done := make(chan struct{})
	go func() {
		x++
		close(done)
	}()
	x++
	<-done
	_ = x
}
