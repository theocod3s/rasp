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

func (c pendingCall) toolCall() llm.ToolCall {
	return llm.ToolCall{ID: c.id, Name: c.name, Input: c.input}
}

func (c pendingCall) event(kind EventKind, res *tool.Result) Event {
	return Event{Kind: kind, CallID: c.id, Tool: c.name, Input: c.input, Result: res}
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

// Approver gates a call before it runs, and is the whole of what the loop knows
// about permissions. What decides the answer — modes, presets, allow-lists,
// grants — stays behind this interface, which is what keeps the loop free of a
// branch on any of it (design §1, §7).
type Approver interface {
	// Prompts reports whether approving this call would put a question to the
	// user. It answers from state alone and asks nothing.
	Prompts(call llm.ToolCall) bool

	// Approve returns nil when the call may run, and otherwise the reason the
	// model is given for a call that did not. A context that ends while it waits
	// comes back as the context's own error: a turn the user stopped is not a
	// call the user refused, and the two are answered differently.
	Approve(ctx context.Context, call llm.ToolCall) error
}

// dispatch runs the calls, writing each result at its own index in a slice sized
// up front. Writing by index rather than appending on completion is what keeps
// tool_result order matched to tool_use order, and every provider rejects a
// request where the two disagree (design §6 rule 6).
//
// The batch is walked in request order and partitioned at every call the user
// has to be asked about: what precedes it runs concurrently and finishes, then
// the question is put, then that call runs on its own (design §6 rule 5). Every
// approval happens here, on the one goroutine driving the batch — which is what
// makes two prompts racing for one terminal impossible rather than unlikely, and
// what leaves a stale Prompts costing a partition boundary and nothing else.
//
// A call nothing ran is left nil for commit to answer, which is what makes a
// cancellation mid-batch a shorter batch rather than a broken transcript.
func (t *turn) dispatch(ctx context.Context, calls []pendingCall, results []*tool.Result) {
	oneAtATime := serial(t.tools, calls)

	var group []int
	flush := func() {
		t.runGroup(ctx, calls, group, results)
		group = group[:0]
	}

	for i, call := range calls {
		if !call.ready {
			continue
		}
		if ctx.Err() != nil {
			break
		}

		asked := t.asks(call)
		if asked {
			flush()
		}
		if err := t.approve(ctx, call); err != nil {
			if ctx.Err() != nil {
				break
			}
			results[i] = t.refuse(call, err)
			continue
		}

		group = append(group, i)
		if asked || oneAtATime {
			flush()
		}
	}
	flush()
}

// runGroup runs one partition of the batch, all of it at once and no more than
// the cap of it at a time.
func (t *turn) runGroup(ctx context.Context, calls []pendingCall, group []int, results []*tool.Result) {
	if len(group) == 0 || ctx.Err() != nil {
		return
	}

	sem := make(chan struct{}, MaxParallelTools)
	var wg sync.WaitGroup
	for _, i := range group {
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
			results[i] = t.invoke(ctx, calls[i])
		}()
	}
	wg.Wait()
}

// gated reports whether the approver decides this call at all. Resolving the
// name comes first (design §5 step 7): one the snapshot does not hold runs
// nothing whatever the answer is, so putting it to the user is a question about
// nothing — and a barrier the rest of the batch would wait behind.
func (t *turn) gated(call pendingCall) bool {
	if t.agent.approver == nil {
		return false
	}
	_, known := t.tools.Get(call.name)
	return known
}

func (t *turn) asks(call pendingCall) bool {
	return t.gated(call) && t.agent.approver.Prompts(call.toolCall())
}

func (t *turn) approve(ctx context.Context, call pendingCall) error {
	if !t.gated(call) {
		return nil
	}
	return t.agent.approver.Approve(ctx, call.toolCall())
}

// refuse answers a call the gate turned down, and announces it as one that
// started and ended: a frontend builds its card out of that pair, and a refusal
// nothing draws reads as the tool having quietly done nothing (design §7.8).
func (t *turn) refuse(call pendingCall, err error) *tool.Result {
	res := &tool.Result{
		IsError: true,
		Content: fmt.Sprintf("%s was not run: %v. The same call again is refused the same way — say "+
			"what it was needed for, or take an approach that does not need it.", call.name, err),
	}
	t.emit(call.event(EventToolStart, nil))
	t.emit(call.event(EventToolEnd, res))
	return res
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
	t.emit(call.event(EventToolStart, nil))
	res := runCall(ctx, t.tools, call)
	t.emit(call.event(EventToolEnd, res))
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
