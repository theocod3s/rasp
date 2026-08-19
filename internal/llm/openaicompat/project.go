package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/openai/openai-go/v3"

	"github.com/theocod3s/rasp/internal/llm"
)

// The finish reasons this wire defines. `function_call` is the deprecated shape
// for the same thing, and no request built here can provoke it.
const (
	wireStop          = "stop"
	wireLength        = "length"
	wireToolCalls     = "tool_calls"
	wireContentFilter = "content_filter"
)

// shape joins the wire's channels to the neutral message's block indexes — two
// numbering spaces with nothing but this table between them (see doc.go).
//
// A channel takes its block the first time it carries anything and that index
// never moves, which is what every accumulation rule rests on (design §3.1a). A
// channel that carries nothing gets no block, and that is not the forbidden drop:
// there is no wire block to drop, because this wire has none.
type shape struct {
	text    int // -1 until content arrives
	refusal int
	tools   map[int]int // tool_calls index -> block index
}

func newShape() *shape { return &shape{text: -1, refusal: -1, tools: map[int]int{}} }

// project maps one chunk onto msg, yielding an event for each piece of content it
// carries; done reports that the consumer has gone.
//
// It walks the chunk rather than the accumulated message so that exactly one block
// grows per event: one chunk can carry fragments for two tool calls, and a delta
// event that grew two blocks is a contract violation.
func (s *shape) project(msg *llm.Message, acc *sdk.ChatCompletionAccumulator, chunk sdk.ChatCompletionChunk,
	yield func(llm.Event) bool) (done bool, err error) {

	if acc.Model != "" {
		msg.Model = acc.Model
	}
	msg.Usage = usage(acc.Usage)

	if len(chunk.Choices) == 0 {
		// The usage-only chunk `stream_options.include_usage` asks for, and every
		// keep-alive an endpoint sends between tokens.
		return false, nil
	}
	if index := chunk.Choices[0].Index; index != 0 {
		return false, fmt.Errorf("a chunk carries choice %d; this adapter asks for one completion "+
			"and has nowhere to put a second", index)
	}
	if len(acc.Choices) == 0 {
		return false, errors.New("a chunk carried a choice the accumulator did not record")
	}
	var (
		delta   = chunk.Choices[0].Delta
		message = acc.Choices[0].Message
	)

	if delta.Content != "" {
		at := s.open(msg, &s.text, llm.Block{Type: llm.BlockText})
		msg.Content[at].Text = message.Content
		if !yield(llm.Event{Type: llm.EventTextDelta, Delta: delta.Content, Partial: msg}) {
			return true, nil
		}
	}
	// A refusal is prose the user has to be able to read, and this wire delivers it
	// in a field of its own rather than as content. Its own block, so neither
	// channel's accumulation is spliced into the other's.
	if delta.Refusal != "" {
		at := s.open(msg, &s.refusal, llm.Block{Type: llm.BlockText})
		msg.Content[at].Text = message.Refusal
		if !yield(llm.Event{Type: llm.EventTextDelta, Delta: delta.Refusal, Partial: msg}) {
			return true, nil
		}
	}

	for _, fragment := range delta.ToolCalls {
		index := toolIndex(fragment.Index)
		if index >= len(message.ToolCalls) {
			return false, fmt.Errorf("a chunk carries a fragment for tool call %d, which the "+
				"accumulator has not opened", index)
		}
		call := message.ToolCalls[index]

		at, open := s.tools[index]
		if !open {
			// Both arrive on a call's first fragment. Kept out of the message rather
			// than committed: a tool_use with no id has nowhere for its result to point
			// (design §4 invariant 1) and one with no name resolves to nothing, so a
			// transcript holding either fails every later request. terminalEvent counts
			// what never opened and fails this turn instead.
			if call.ID == "" || call.Function.Name == "" {
				continue
			}
			at = len(msg.Content)
			s.tools[index] = at
			msg.Content = append(msg.Content, llm.Block{Type: llm.BlockToolUse})
		}
		// Re-read rather than taken once: the SDK's accumulator *concatenates* a
		// call's function name across fragments, so a name split in two would be
		// committed as its first half. Safe after the announcement freezes the block,
		// because a call the accumulator called finished receives no more fragments.
		msg.Content[at].ID, msg.Content[at].Name = call.ID, call.Function.Name
		if !open {
			if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
				return true, nil
			}
		}

		// An empty fragment is how this wire opens a call's arguments, and it arrives
		// again on the chunk after. Announcing nothing twice would have a UI drawing
		// an argument stream that never moved.
		if fragment.Function.Arguments == "" {
			continue
		}
		msg.Content[at].Input = json.RawMessage(call.Function.Arguments)
		if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: fragment.Function.Arguments, Partial: msg}) {
			return true, nil
		}
	}
	return false, nil
}

func (s *shape) open(msg *llm.Message, at *int, block llm.Block) int {
	if *at < 0 {
		*at = len(msg.Content)
		msg.Content = append(msg.Content, block)
	}
	return *at
}

// toolIndex is the wire's own index for a tool call. Bedrock-style gateways send
// -1 for a single call, which the SDK's accumulator folds onto 0 — so anything
// reading its output has to fold the same way or address a call it never wrote.
func toolIndex(index int64) int {
	if index < 0 {
		return 0
	}
	return int(index)
}

// usage maps the wire's counts onto the neutral ones. `prompt_tokens` counts the
// whole prompt including whatever came from cache, where llm.Usage.Input excludes
// it — so the cached half is subtracted rather than counted twice.
//
// CacheWrite stays zero: this wire has no count for one, caching here being
// automatic rather than requested. A cache write is therefore estimated as
// ordinary input (design §11).
func usage(u sdk.CompletionUsage) llm.Usage {
	cached := int(u.PromptTokensDetails.CachedTokens)
	input := int(u.PromptTokens) - cached
	if input < 0 {
		// More cached than prompt tokens. Clamped: a negative count falls below the
		// previous report, which the stream contract reads as usage revised downward.
		input, cached = int(u.PromptTokens), 0
	}
	return llm.Usage{Input: input, Output: int(u.CompletionTokens), CacheRead: cached}
}

func finishReason(chunk sdk.ChatCompletionChunk) string {
	if len(chunk.Choices) == 0 {
		return ""
	}
	return chunk.Choices[0].FinishReason
}

// calls decides when each tool call is announced, in block order. Results are
// answered in announcement order and persisted in block order, so the two have to
// agree — and this wire may interleave fragments, so calls do not finish in the
// order they opened.
type calls struct {
	done map[int]bool // block indexes whose arguments have finished arriving
	at   int          // how far through Partial announcing has reached
	all  bool         // the stream ended: everything still open has finished
}

func (c *calls) finish(at int) {
	if c.done == nil {
		c.done = map[int]bool{}
	}
	c.done[at] = true
}

func (c *calls) finishAll() { c.all = true }

// ready is the calls whose turn it is to be announced. It walks forward only: a
// block already announced, or passed over because its arguments never finished, is
// never revisited.
func (c *calls) ready(msg *llm.Message) []*llm.ToolCall {
	var out []*llm.ToolCall
	for ; c.at < len(msg.Content); c.at++ {
		block := msg.Content[c.at]
		if block.Type != llm.BlockToolUse {
			continue
		}
		if !c.all && !c.done[c.at] {
			return out
		}
		// Unfinished arguments stay in the message — dropping a block moves an index —
		// and are never announced, so the loop never runs that call.
		args := arguments(block.Input)
		if args == nil {
			continue
		}
		msg.Content[c.at].Input = args
		// Cloned rather than aliased, for the reason llm.Event.ToolCall gives: each
		// announcement is its own value, and the arguments are half of one.
		out = append(out, &llm.ToolCall{ID: block.ID, Name: block.Name, Input: bytes.Clone(args)})
	}
	return out
}

// arguments is a call's arguments once they have finished arriving, or nil if they
// have not. An object rather than merely valid JSON, because `null` unmarshals into
// a struct as a silent no-op and would run a tool with every argument zeroed.
//
// Nothing at all becomes `{}`: this wire opens a call's arguments with an empty
// string, and a call taking none never sends another fragment — so here the empty
// string is a finished payload rather than a fragment cut short.
func arguments(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil
	}
	return raw
}

// terminalEvent ends a stream the server finished, and is the last place a
// response that cannot be committed can still be refused.
func terminalEvent(msg *llm.Message, acc *sdk.ChatCompletionAccumulator, s *shape) (llm.Event, error) {
	if len(acc.Choices) == 0 {
		return llm.Event{}, errors.New("the stream ended without a completion in it")
	}
	// Derived rather than counted as it happened: every call the accumulator holds
	// should have opened a block, and the only ones project refuses to open are
	// those with no id or no function name.
	if missing := len(acc.Choices[0].Message.ToolCalls) - len(s.tools); missing > 0 {
		return llm.Event{}, fmt.Errorf("%d of the %d tool calls arrived with no id or no function "+
			"name, so nothing could run or answer them", missing, len(acc.Choices[0].Message.ToolCalls))
	}
	reason := acc.Choices[0].FinishReason
	if reason == "" {
		return llm.Event{}, errors.New("the stream ended without a finish reason")
	}
	stop, err := stopReason(reason, len(s.tools))
	if err != nil {
		return llm.Event{}, err
	}

	// A finished turn holding a call whose arguments are not a JSON object has no
	// good answer: unannounced, the loop never writes the tool_result the block
	// requires and every later request fails (design §4 invariant 1); announced, it
	// runs a tool with arguments that mean nothing. Truncation is exempt — design §4
	// invariant 2 fails that call and answers it.
	if stop == llm.StopEndTurn || stop == llm.StopToolUse {
		for index, block := range msg.Content {
			if block.Type == llm.BlockToolUse && arguments(block.Input) == nil {
				return llm.Event{}, fmt.Errorf("the turn finished with call %q (block %d) holding "+
					"arguments that are not a JSON object: %s", block.ID, index, block.Input)
			}
		}
	}

	msg.StopReason = stop
	return llm.Event{Type: llm.EventDone, StopReason: stop, Partial: msg}, nil
}

// stopReason needs the tool call count to answer. `stop` alongside tool calls is
// what Ollama and llama.cpp-style servers send where OpenAI sends `tool_calls`, and
// reading it as end_turn presents a turn that stopped to call a tool as a finished
// answer — after which nothing takes the next step. An unmapped reason is an error
// for the same reason (decisions.md).
func stopReason(reason string, uses int) (llm.StopReason, error) {
	switch reason {
	case wireStop:
		if uses > 0 {
			return llm.StopToolUse, nil
		}
		return llm.StopEndTurn, nil
	case wireToolCalls:
		if uses == 0 {
			return "", errors.New("the stream reported the model stopped to use a tool and carried none")
		}
		return llm.StopToolUse, nil
	case wireLength:
		return llm.StopMaxTokens, nil
	case wireContentFilter:
		return llm.StopRefusal, nil
	}
	return "", fmt.Errorf("unsupported finish reason %q", reason)
}

// errorEvent ends a stream that failed. Cancellation is separated out because
// design §4's termination table treats it as a completion, which the retry
// classifier must not read as a transport failure worth another turn.
//
// An expired deadline is deliberately NOT cancellation: §4 commits an aborted turn
// and never retries it, while §12 counts a timeout among the retryable failures.
func errorEvent(msg *llm.Message, err error) llm.Event {
	reason := llm.StopError
	if errors.Is(err, context.Canceled) {
		reason = llm.StopAborted
	}
	msg.StopReason = reason
	return llm.Event{Type: llm.EventError, StopReason: reason, Err: err, Partial: msg}
}
