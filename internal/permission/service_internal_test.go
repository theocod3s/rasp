package permission

import "testing"

// TestAnAbandonedPromptTakesNoAnswer covers the window Ask closes when its
// context ends: the prompt is struck out before it is unregistered, so an answer
// already on its way is told it decided nothing rather than being dropped
// silently into a channel the asker has stopped reading.
func TestAnAbandonedPromptTakesNoAnswer(t *testing.T) {
	p := &pending{reply: make(chan Decision, 1)}
	p.abandon()

	if p.answer(DecisionOnce) {
		t.Errorf("an answer to an abandoned prompt reported deciding it")
	}
}
