package permission

import (
	"errors"
	"testing"
)

// recordingRules reports whether the preset was consulted at all — the half of
// rung 0 that an allow-or-deny assertion cannot see.
type recordingRules struct {
	consulted bool
	rule      Rule
}

func (r *recordingRules) Resolve(Request) Rule {
	r.consulted = true
	return r.rule
}

// TestYoloAnswersBeforeEveryOtherRung holds design §7.7's reason for making the
// bypass a field rather than a preset that allows everything: under yolo no
// pattern is consulted, so no pattern can deny.
//
// Nothing turns the field on outside this package yet, which is why the test is
// in it — the flag and the command that will are their own work, and the
// ordering is what had to be right first.
func TestYoloAnswersBeforeEveryOtherRung(t *testing.T) {
	req := Request{CallID: "call-1", Tool: "bash", Action: ActionExecute, Command: "rm -rf /"}

	preset := &recordingRules{rule: RuleDeny}
	s := New(nil) // reaching rung 5 with no Prompter is a refusal as well
	s.SetRules(preset)

	if err := s.Ask(t.Context(), req); !errors.Is(err, ErrDenied) {
		t.Fatalf("Ask with the bypass off = %v, want ErrDenied — otherwise what follows proves nothing", err)
	}

	preset.consulted = false
	s.yolo.Store(true)

	if err := s.Ask(t.Context(), req); err != nil {
		t.Errorf("Ask under yolo = %v, want the call allowed", err)
	}
	if preset.consulted {
		t.Errorf("the preset was consulted under yolo, so a pattern could still deny")
	}
}

// TestRemovingThePresetLeavesTheQuestionToTheRungsBelow covers the one call
// SetRules cannot pass straight to the atomic pointer: a nil Rules stored as a
// pointer to a nil interface reads back as present and panics on use.
func TestRemovingThePresetLeavesTheQuestionToTheRungsBelow(t *testing.T) {
	req := Request{CallID: "call-1", Tool: "write", Action: ActionWrite, Path: "/foo/a.go"}

	s := New(nil)
	s.SetRules(&recordingRules{rule: RuleAllow})
	if err := s.Ask(t.Context(), req); err != nil {
		t.Fatalf("Ask under a preset that allows = %v, want the call allowed", err)
	}

	s.SetRules(nil)
	if err := s.Ask(t.Context(), req); !errors.Is(err, ErrDenied) {
		t.Errorf("Ask with no preset = %v, want the rungs below to answer", err)
	}
}

// TestAnAbandonedPromptTakesNoAnswer covers the window Ask closes when its
// context ends: the prompt is struck out before it is unregistered, so an answer
// already on its way is told it decided nothing rather than being dropped
// silently into a channel the asker has stopped reading.
func TestAnAbandonedPromptTakesNoAnswer(t *testing.T) {
	p := &pending{reply: make(chan Decision, 1)}
	p.abandon()

	if p.answer(DecideOnce) {
		t.Errorf("an answer to an abandoned prompt reported deciding it")
	}
}
