package agent

import (
	"context"
	"testing"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tool"
)

// TestCommitAnswersACallTheDispatchListLeftOut is the reason commit walks the
// message it is committing rather than the list that was dispatched. Nothing in
// the loop hands it a short list today; the guards and the partitioning still to
// land on this file are what could, and the failure that produces is a bricked
// session rather than a wrong answer.
//
// Reaching into the package because that gap is not visible from outside it: a
// turn driven through Send always has one call per tool_use block, which is the
// property under test rather than a fixture for it.
func TestCommitAnswersACallTheDispatchListLeftOut(t *testing.T) {
	cases := []struct {
		name    string
		calls   []pendingCall
		results []*tool.Result
	}{
		{"a call the list never mentioned",
			[]pendingCall{{id: "call_1"}},
			[]*tool.Result{{Content: "package main"}}},
		{"a list longer than its results",
			[]pendingCall{{id: "call_1"}, {id: "call_2"}},
			[]*tool.Result{{Content: "package main"}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Agent{}
			msg := &llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
				{Type: llm.BlockToolUse, ID: "call_1", Name: "read"},
				{Type: llm.BlockToolUse, ID: "call_2", Name: "write"},
			}}

			(&turn{agent: a}).commit(msg, c.calls, c.results, unanswered)

			msgs := a.Messages()
			if len(msgs) != 2 {
				t.Fatalf("commit wrote %d message(s); a reply that asked for tools and the message "+
					"answering it are two", len(msgs))
			}
			if got := len(msgs[1].Content); got != 2 {
				t.Fatalf("the answering message holds %d block(s) for two tool_use blocks", got)
			}
			for i, want := range []string{"call_1", "call_2"} {
				if got := msgs[1].Content[i]; got.Type != llm.BlockToolResult || got.ToolUseID != want {
					t.Errorf("block %d answers %q as a %q; the block is what asks for a result, so "+
						"every one of them gets one", i, got.ToolUseID, got.Type)
				}
			}
			if got := msgs[1].Content[1]; got.Content != unanswered || !got.IsError {
				t.Errorf("the call nothing ran was answered with %q (error: %t)", got.Content, got.IsError)
			}
		})
	}
}

// TestUnrunTellsTheModelWhichKindOfNothingHappened: a turn the user stopped is
// the one case the model can act on differently.
func TestUnrunTellsTheModelWhichKindOfNothingHappened(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if got := unrun(ctx, llm.StopToolUse); got != unanswered {
		t.Errorf("a call nothing ran on a live turn says %q", got)
	}
	if got := unrun(ctx, llm.StopAborted); got != interrupted {
		t.Errorf("a call on a turn the model reported aborted says %q", got)
	}
	if got := unrun(ctx, llm.StopMaxTokens); got != truncated {
		t.Errorf("a call from a reply the output limit cut short says %q", got)
	}

	cancel()
	if got := unrun(ctx, llm.StopToolUse); got != interrupted {
		t.Errorf("a call skipped because the turn was cancelled says %q", got)
	}
	if got := unrun(ctx, llm.StopMaxTokens); got != truncated {
		t.Errorf("a truncated reply's call on a cancelled turn says %q; the guard refused the batch "+
			"before the cancellation could have skipped it, and repeating the call is still the one "+
			"thing that cannot work", got)
	}
}
