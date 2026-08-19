package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tool"
)

// What a call that produced no result tells the model. These are prompt text, so
// each says what happened and that asking again is the useful next move
// (internals §2.4).
const (
	interrupted = "this tool call was interrupted and did not produce a result; ask for it again if you still need it."
	unanswered  = "this tool call did not run and produced no result; ask for it again if you still need it."

	// The same call again is the one move that cannot work here, so this one says
	// what has to change rather than only that nothing ran.
	truncated = "this tool call did not run: the reply reached its output limit part way through, so the " +
		"arguments of every call in it may be cut off mid-value and mean something other than they say. " +
		"Ask again with less in one reply — fewer calls, or the work split into smaller pieces."
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

// The window, and how many of it one signature may fill (design §4 invariant 3).
// Counting over a window rather than a run is what survives an interleaved second
// call, which resets a consecutive-repeat rule and leaves the pair spinning; the
// cost is that two signatures alternating exactly evenly sit at 5 of 10 and are
// left to the step fuse.
const (
	loopWindow  = 10
	loopRepeats = 5
)

type signature [sha256.Size]byte

// loopDetector holds the last loopWindow tool-call signatures. One lives per
// turn: a new prompt is a person redirecting the work, so a call repeated after
// one is an instruction rather than a runaway.
type loopDetector struct{ recent []signature }

// observe records a finished call and reports how many of the window match it.
//
// The hash is what bounds the window — a call that read a large file would
// otherwise be held in memory ten times over — and the parts are length-prefixed
// so a name running into its arguments cannot collide with a different split of
// the same bytes. The call id is deliberately absent: ids are unique per call, so
// a signature carrying one would never repeat.
func (d *loopDetector) observe(name string, input json.RawMessage, output string) int {
	h := sha256.New()
	for _, part := range []string{name, string(input), output} {
		fmt.Fprintf(h, "%d:%s", len(part), part)
	}
	sig := signature(h.Sum(nil))

	d.recent = append(d.recent, sig)
	if len(d.recent) > loopWindow {
		d.recent = d.recent[1:]
	}

	matches := 0
	for _, s := range d.recent {
		if s == sig {
			matches++
		}
	}
	return matches
}

// checkLooping is design §4's step 9: a signature that has taken over the recent
// window ends the turn with something a person can read, rather than leaving it
// to the step fuse ninety-odd calls later.
//
// Calls nothing ran are skipped, because what answers those is what the turn did
// rather than what the call produced. The batch is walked in index order, so what
// the window holds does not depend on the order a concurrent one finishes in.
func (t *turn) checkLooping(calls []pendingCall, results []*tool.Result) error {
	for i := 0; i < len(calls) && i < len(results); i++ {
		if results[i] == nil {
			continue
		}
		matches := t.loops.observe(calls[i].name, calls[i].input, results[i].Content)
		if matches <= loopRepeats {
			continue
		}
		return fmt.Errorf("%w: %q was called %d times in the last %d tool calls with the same "+
			"arguments and the same result, so running it again would produce the same nothing",
			ErrLooping, calls[i].name, matches, loopWindow)
	}
	return nil
}

// unrun is what this step's unanswered calls say. A turn the user stopped is the
// one case the model can act on differently, and it is the wording design §4's
// prevent-on-write sketch and internals §2.4's repair-on-read one both carry.
//
// Truncation is asked first because it is why nothing ran whatever else is true:
// the guard refuses the batch on the stop reason alone, before a cancellation
// arriving in the same window could have skipped it (design §4 invariant 2).
func unrun(ctx context.Context, stop llm.StopReason) string {
	switch {
	case stop == llm.StopMaxTokens:
		return truncated
	case stop == llm.StopAborted || ctx.Err() != nil:
		return interrupted
	}
	return unanswered
}
