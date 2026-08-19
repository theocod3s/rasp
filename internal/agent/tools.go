package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tool"
)

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

// MaxParallelTools is how many of a batch's calls run at once. Unbounded
// goroutines against a slow filesystem is its own failure mode (design §6 rule 4).
const MaxParallelTools = 8

// dispatch runs the calls, writing each result at its own index in a slice sized
// up front. Writing by index rather than appending on completion is what keeps
// tool_result order matched to tool_use order, and every provider rejects a
// request where the two disagree (design §6 rule 6).
//
// A call nothing ran is left nil for commit to answer, which is what makes a
// cancellation mid-batch a shorter batch rather than a broken transcript.
func (t *turn) dispatch(ctx context.Context, calls []pendingCall, results []*tool.Result) {
	if serial(t.tools, calls) {
		for i, call := range calls {
			if !call.ready {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			results[i] = t.invoke(ctx, call)
		}
		return
	}

	sem := make(chan struct{}, MaxParallelTools)
	var wg sync.WaitGroup
	for i, call := range calls {
		if !call.ready {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			// Checked again on the far side of the semaphore, because select picks
			// between two ready cases at random: a slot freeing as the turn is
			// cancelled would otherwise start a call after the interrupt half the time.
			if ctx.Err() != nil {
				return
			}
			results[i] = t.invoke(ctx, call)
		}()
	}
	wg.Wait()
}

// serial reports whether any call in the batch forces the whole of it sequential.
// One such call and every sibling waits, rather than that call merely running
// alone: a tool declares itself sequential because it touches state it does not
// own, and running it beside unaudited siblings is the thing it is declaring
// against (design §6 rule 4). A name the snapshot does not hold has no tool to
// ask, and comes back as an error either way.
func serial(tools *tool.Set, calls []pendingCall) bool {
	for _, call := range calls {
		if !call.ready {
			continue
		}
		impl, ok := tools.Get(call.name)
		if !ok {
			continue
		}
		if s, declares := impl.(tool.Sequential); declares && s.Sequential() {
			return true
		}
	}
	return false
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
