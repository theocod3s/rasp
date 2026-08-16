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
func project(msg *llm.Message, acc *sdk.Message) {
	msg.Usage = llm.Usage{
		Input:      int(acc.Usage.InputTokens),
		Output:     int(acc.Usage.OutputTokens),
		CacheRead:  int(acc.Usage.CacheReadInputTokens),
		CacheWrite: int(acc.Usage.CacheCreationInputTokens),
	}
	if acc.Model != "" {
		msg.Model = string(acc.Model)
	}

	// Filled by index rather than rebuilt, so the slice a consumer is holding
	// grows instead of being replaced underneath it.
	at := 0
	for i := range acc.Content {
		block, ok := projectBlock(&acc.Content[i])
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
}

// projectBlock maps one wire block, reporting false for a type the neutral
// message has nowhere to put. A redacted_thinking block carries nothing to draw,
// and tool_use cannot arrive because buildParams sends no tools.
func projectBlock(block *sdk.ContentBlockUnion) (llm.Block, bool) {
	switch block.Type {
	case "text":
		return llm.Block{Type: llm.BlockText, Text: block.Text}, true
	case "thinking":
		return llm.Block{Type: llm.BlockThinking, Text: block.Thinking}, true
	}
	return llm.Block{}, false
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

// stopReasons covers what a request built here can provoke. pause_turn needs a
// server-side tool and model_context_window_exceeded is a failure, so both take
// the unsupported path above rather than a mapping that would read as success.
var stopReasons = map[sdk.StopReason]llm.StopReason{
	sdk.StopReasonEndTurn:      llm.StopEndTurn,
	sdk.StopReasonStopSequence: llm.StopEndTurn,
	sdk.StopReasonMaxTokens:    llm.StopMaxTokens,
	sdk.StopReasonToolUse:      llm.StopToolUse,
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
