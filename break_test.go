package rasp_test

import "testing"

// Deliberately wrong: %d against a string is exactly what go vet exists to catch.
func TestBreakVet(t *testing.T) {
	t.Logf("%d", "not a number")
}
