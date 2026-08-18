package anthropic

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/theocod3s/rasp/internal/llm"
)

// project copies the accumulated wire message onto the neutral one, in place.
//
// Usage comes from the accumulator and never from the event that just arrived.
// message_delta carries output_tokens alone, so an adapter reading the event
// would zero the input and cache counts on the last event of every turn — and
// Message.Usage is what context estimation trusts from there on (design §11).
func project(msg *llm.Message, acc *sdk.Message) error {
	msg.Usage = llm.Usage{
		Input:      int(acc.Usage.InputTokens),
		Output:     int(acc.Usage.OutputTokens),
		CacheRead:  int(acc.Usage.CacheReadInputTokens),
		CacheWrite: int(acc.Usage.CacheCreationInputTokens),
	}
	if acc.Model != "" {
		msg.Model = string(acc.Model)
	}

	// Filled by index rather than rebuilt: after the first few events the blocks
	// are already there, so this costs no allocation per event. Same argument as
	// the one message allocated per call.
	at := 0
	for i := range acc.Content {
		block, ok, err := projectBlock(&acc.Content[i])
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if at < len(msg.Content) {
			msg.Content[at] = block
		} else {
			msg.Content = append(msg.Content, block)
		}
		at++
	}
	return nil
}

// projectBlock maps one wire block: a value, or false for one deliberately
// dropped, or an error for a type nobody has taught this adapter.
//
// The three-way split is the point. Dropping an unknown type silently is the
// quietest failure the receive side could have — the user reads an answer with a
// hole in it, and a turn whose only block was unknown commits with nothing in it,
// which the send side then withholds from every later request, so the reply leaves
// no trace at all. Anthropic adds block types regularly, so the default has to be
// loud.
//
// Drop on the block's TYPE and never on its contents. Projection is positional,
// and the SDK documents that deltas and stops interleave across open blocks,
// addressing them by index rather than by whichever started last. A type is fixed
// the moment the block opens, so dropping on it shifts later blocks by the same
// amount at every event and no index ever moves. Contents are not: hold an empty
// block back and the next block to receive text takes its index, then the held-back
// block fills and everything after it shifts. Partial reads as rewritten, which the
// stream contract rejects — correctly, since a consumer already drew the old text.
func projectBlock(block *sdk.ContentBlockUnion) (llm.Block, bool, error) {
	switch block.Type {
	case "text":
		return llm.Block{Type: llm.BlockText, Text: block.Text}, true, nil
	case "thinking":
		return llm.Block{Type: llm.BlockThinking, Text: block.Thinking}, true, nil
	case "redacted_thinking":
		// Encrypted reasoning. Dropped rather than refused because it is known to
		// carry nothing drawable — the one type where silence loses nothing.
		return llm.Block{}, false, nil
	}
	return llm.Block{}, false, fmt.Errorf("anthropic: unsupported content block %q", block.Type)
}

// neutralEvent maps one wire event onto the event union, reporting false for
// those with no counterpart.
func neutralEvent(event sdk.MessageStreamEventUnion) (llm.Event, bool) {
	switch event.Type {
	case "message_start":
		return llm.Event{Type: llm.EventMessageStart}, true
	case "content_block_delta":
		switch event.Delta.Type {
		case "text_delta":
			return llm.Event{Type: llm.EventTextDelta, Delta: event.Delta.Text}, true
		case "thinking_delta":
			return llm.Event{Type: llm.EventThinkingDelta, Delta: event.Delta.Thinking}, true
		}
	}
	return llm.Event{}, false
}

// terminalEvent ends a stream the server finished.
func terminalEvent(msg *llm.Message, reason sdk.StopReason) llm.Event {
	if reason == "" {
		return errorEvent(msg, errors.New("anthropic: stream ended without a stop reason"))
	}
	// The one unmapped reason a long session reaches routinely, and §12 wants it
	// fatal with a fix hint rather than retried. The text below reaches the user; it
	// does NOT reach the retry classifier, which reads a Message, and a Message
	// carries no error. Telling this apart from a stream that merely ended is still
	// open, and it is the classifier's shape to settle, not this file's.
	if reason == sdk.StopReasonModelContextWindowExceeded {
		return errorEvent(msg, errors.New("anthropic: the conversation is longer than the model's "+
			"context window; it has to be compacted or started again"))
	}
	stop, ok := stopReasons[reason]
	if !ok {
		// end_turn is the tempting default and the wrong one: it would present a
		// turn that stopped for a reason we do not model as a finished answer, and
		// the retry classifier would never look at it again (design §12).
		return errorEvent(msg, fmt.Errorf("anthropic: unsupported stop reason %q", reason))
	}
	msg.StopReason = stop
	return llm.Event{Type: llm.EventDone, StopReason: stop, Partial: msg}
}

// stopReasons covers what a request built here can provoke. Everything else takes
// the unsupported path above rather than a mapping that would read as success:
// pause_turn needs a server-side tool, model_context_window_exceeded is a failure,
// and tool_use would be the quietest of the three — projectBlock cannot represent
// a tool_use block, so the loop would be told the model stopped to call a tool
// with no call anywhere in the message.
var stopReasons = map[sdk.StopReason]llm.StopReason{
	sdk.StopReasonEndTurn:      llm.StopEndTurn,
	sdk.StopReasonStopSequence: llm.StopEndTurn,
	sdk.StopReasonMaxTokens:    llm.StopMaxTokens,
	sdk.StopReasonRefusal:      llm.StopRefusal,
}

// errorEvent ends a stream that failed. Cancellation is separated out because
// design §4's termination table treats it as a completion, and the retry
// classifier must not read it as a transport failure worth another turn.
//
// An expired deadline is deliberately NOT cancellation. §4 commits an aborted
// turn and never retries it, while §12 counts a timeout among the retryable
// failures — so calling one an abort would present a turn that timed out as one
// the user interrupted, and take it out of the retry path on the way.
func errorEvent(msg *llm.Message, err error) llm.Event {
	reason := llm.StopError
	if errors.Is(err, context.Canceled) {
		reason = llm.StopAborted
	}
	msg.StopReason = reason
	return llm.Event{Type: llm.EventError, StopReason: reason, Err: err, Partial: msg}
}
