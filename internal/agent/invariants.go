package agent

import (
	"context"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tool"
)

// What a call that produced no result tells the model. These are prompt text, so
// each says what happened and that asking again is the useful next move
// (internals §2.4).
const (
	interrupted = "this tool call was interrupted and did not produce a result; ask for it again if you still need it."
	unanswered  = "this tool call did not run and produced no result; ask for it again if you still need it."
)

// commit writes the step: the assistant message, and the message answering every
// tool_use block it holds, in one append or neither. An orphaned tool_use is a
// 400 on this request and on every request built from the transcript afterwards,
// so the session is bricked rather than degraded (design §4 invariant 1).
//
// The answers come from walking the message being committed, not the list that
// was dispatched, which is what makes the pairing structural rather than merely
// true today: a call a guard refuses or a partition loses is still answered,
// because the block is what asks for one. This is the loop's only append of a
// model's reply, and that is the point.
func (t *turn) commit(msg *llm.Message, calls []pendingCall, results []*tool.Result, noResult string) {
	var answers []llm.Block
	for _, block := range msg.Content {
		if block.Type != llm.BlockToolUse {
			continue
		}
		res := tool.Result{IsError: true, Content: noResult}
		if ran := resultFor(block.ID, calls, results); ran != nil {
			res = *ran
		}
		answers = append(answers, llm.Block{
			Type:      llm.BlockToolResult,
			ToolUseID: block.ID,
			Content:   res.Content,
			IsError:   res.IsError,
		})
	}

	if len(answers) == 0 {
		t.agent.append(*msg)
		return
	}
	t.agent.append(*msg, llm.Message{Role: llm.RoleUser, Content: answers})
}

// resultFor finds what a call produced, or nil for one nothing ran. The bound on
// results is read rather than assumed, so a pair of slices that somehow is not
// sized together leaves calls unanswered rather than panicking mid-commit.
func resultFor(id string, calls []pendingCall, results []*tool.Result) *tool.Result {
	for i := 0; i < len(calls) && i < len(results); i++ {
		if calls[i].id == id {
			return results[i]
		}
	}
	return nil
}

// unrun is what this step's unanswered calls say. A turn the user stopped is the
// one case the model can act on differently, and it is the wording design §4's
// prevent-on-write sketch and internals §2.4's repair-on-read one both carry.
func unrun(ctx context.Context, stop llm.StopReason) string {
	if stop == llm.StopAborted || ctx.Err() != nil {
		return interrupted
	}
	return unanswered
}
