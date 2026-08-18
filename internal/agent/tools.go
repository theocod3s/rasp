package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tool"
)

// unanswered is what a call that never ran tells the model. Every string that
// reaches the model is prompt text, so it says what happened and what to do
// about it: asking again is usually the useful next move.
const unanswered = "this tool call did not run and produced no result; ask for it again if you still need it."

// pendingCall is one tool_use block, and the announcement that completed its
// arguments if one arrived.
type pendingCall struct {
	id    string
	name  string
	input json.RawMessage
	ready bool
}

// toolUses pairs the message's tool_use blocks with the calls the stream
// announced, in block order.
//
// The blocks decide, not the announcements: a block with no announcement is a
// call whose arguments never finished arriving, and it still owes the model a
// result, while an announcement matching no block has nothing for a result to
// name.
func toolUses(msg *llm.Message, announced []llm.ToolCall) []pendingCall {
	byID := make(map[string]llm.ToolCall, len(announced))
	for _, call := range announced {
		byID[call.ID] = call
	}

	var calls []pendingCall
	for _, block := range msg.Content {
		if block.Type != llm.BlockToolUse {
			continue
		}
		call := pendingCall{id: block.ID, name: block.Name}
		if announced, ok := byID[block.ID]; ok {
			call.name, call.input, call.ready = announced.Name, announced.Input, true
		}
		calls = append(calls, call)
	}
	return calls
}

// dispatch runs the calls, writing each result at its own index in a slice sized
// up front. Writing by index rather than appending on completion is what keeps
// tool_result order matched to tool_use order once these run concurrently, and
// every provider rejects a request where the two disagree (design §6 rule 6).
func (t *turn) dispatch(ctx context.Context, calls []pendingCall, results []*tool.Result) {
	for i, call := range calls {
		if !call.ready {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		results[i] = t.invoke(ctx, call)
	}
}

func (t *turn) invoke(ctx context.Context, call pendingCall) *tool.Result {
	t.emit(Event{Kind: EventToolStart, CallID: call.id, Tool: call.name, Input: call.input})
	res := runCall(ctx, t.tools, call)
	t.emit(Event{Kind: EventToolEnd, CallID: call.id, Tool: call.name, Input: call.input, Result: res})
	return res
}

func runCall(ctx context.Context, tools *tool.Set, call pendingCall) *tool.Result {
	impl, ok := tools.Get(call.name)
	if !ok {
		// The model asked for something that is not on the list it was given, and
		// that is a conversation rather than a failure: it can read this and pick
		// a tool that exists (design §12).
		return &tool.Result{
			IsError: true,
			Content: fmt.Sprintf("there is no tool named %q; call one of the tools this request lists.", call.name),
		}
	}

	res, err := tool.RunSafely(ctx, impl, call.input)
	if err != nil {
		return &tool.Result{
			IsError: true,
			Content: fmt.Sprintf("%s could not run: %v", call.name, err),
		}
	}
	return &res
}

// resultBlocks answers every call in the order the model asked, including the
// ones nothing ran.
func resultBlocks(calls []pendingCall, results []*tool.Result) []llm.Block {
	blocks := make([]llm.Block, len(calls))
	for i, call := range calls {
		res := tool.Result{IsError: true, Content: unanswered}
		if ran := results[i]; ran != nil {
			res = *ran
		}
		blocks[i] = llm.Block{
			Type:      llm.BlockToolResult,
			ToolUseID: call.id,
			Content:   res.Content,
			IsError:   res.IsError,
		}
	}
	return blocks
}
