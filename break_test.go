package rasp_test

import "testing"

// Deliberately failing, to prove `go test ./...` can turn the job red.
func TestBreakTest(t *testing.T) {
	t.Fatal("deliberate failure")
}
