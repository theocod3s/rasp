package llm_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/theocod3s/rasp/internal/llm"
)

// TestMain runs the leak detector over the package. A stream is an iterator, so
// nothing here needs a goroutine — but a consumer abandoning a stream halfway is
// exactly the shape that leaks one, and the check has to be in place before the
// first provider that pumps events from a background reader arrives.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// scripted is a provider that plays a fixed list of actions. It is not
// internal/llm/fake, which gets a friendlier surface and enough scripting to
// drive the agent loop; this one stays small enough to read in one sitting, so a
// test failure is never ambiguous about which side is wrong.
type scripted struct {
	id      string
	actions []action
}

func (s scripted) ID() string { return s.id }

// Stream plays the script. The message is allocated HERE, before the first
// yield, so the tests below check a producer built the way the contract asks
// rather than one built to pass them.
func (s scripted) Stream(ctx context.Context, _ llm.Request) llm.StreamResponse {
	return func(yield func(llm.Event) bool) {
		msg := &llm.Message{Role: llm.RoleAssistant, Model: "scripted-1", Provider: s.id}

		if !yield(llm.Event{Type: llm.EventMessageStart, Partial: msg}) {
			return
		}
		for _, act := range s.actions {
			// A cancelled turn leaves through the same door as any other runtime
			// failure: a terminal EventError.
			if err := ctx.Err(); err != nil {
				fail(err, llm.StopAborted)(msg, yield)
				return
			}
			if !act(msg, yield) {
				return
			}
			// Only done and fail set a stop reason, and nothing may follow a
			// terminal event, so the rest of the script is discarded.
			if msg.StopReason != "" {
				return
			}
		}
	}
}

// action is one scripted step: it mutates the accumulating message and yields
// the events that go with it, returning false once the consumer has stopped
// listening.
type action func(msg *llm.Message, yield func(llm.Event) bool) bool

// text and thinking stream one block each, one event per chunk, the way a
// provider splits a sentence at arbitrary boundaries. The pairing of block type
// to event type lives in one place here, as it does in checkAccumulation, since
// a provider that pours one channel into the other is what the contract watches
// for.
func text(chunks ...string) action {
	return deltas(llm.BlockText, llm.EventTextDelta, chunks)
}

func thinking(chunks ...string) action {
	return deltas(llm.BlockThinking, llm.EventThinkingDelta, chunks)
}

func deltas(kind llm.BlockType, event llm.EventType, chunks []string) action {
	return func(msg *llm.Message, yield func(llm.Event) bool) bool {
		msg.Content = append(msg.Content, llm.Block{Type: kind})
		at := len(msg.Content) - 1
		for _, chunk := range chunks {
			msg.Content[at].Text += chunk
			if !yield(llm.Event{Type: event, Delta: chunk, Partial: msg}) {
				return false
			}
		}
		return true
	}
}

// toolCall streams a call the way one actually arrives: a start, the argument
// JSON in fragments that do not individually parse, then one complete call.
func toolCall(id, name string, fragments ...string) action {
	return func(msg *llm.Message, yield func(llm.Event) bool) bool {
		msg.Content = append(msg.Content, llm.Block{Type: llm.BlockToolUse, ID: id, Name: name})
		at := len(msg.Content) - 1

		if !yield(llm.Event{Type: llm.EventToolInputStart, Partial: msg}) {
			return false
		}
		var input string
		for _, frag := range fragments {
			input += frag
			msg.Content[at].Input = json.RawMessage(input)
			if !yield(llm.Event{Type: llm.EventToolInputDelta, Delta: frag, Partial: msg}) {
				return false
			}
		}
		return yield(llm.Event{
			Type:     llm.EventToolCall,
			Partial:  msg,
			ToolCall: &llm.ToolCall{ID: id, Name: name, Input: json.RawMessage(input)},
		})
	}
}

func done(reason llm.StopReason) action {
	return func(msg *llm.Message, yield func(llm.Event) bool) bool {
		msg.StopReason = reason
		return yield(llm.Event{Type: llm.EventDone, StopReason: reason, Partial: msg})
	}
}

// fail ends the stream with a failure. Note what it does NOT do: return an error
// to the caller of Stream. There is nowhere to return one to.
func fail(err error, reason llm.StopReason) action {
	return func(msg *llm.Message, yield func(llm.Event) bool) bool {
		msg.StopReason = reason
		return yield(llm.Event{Type: llm.EventError, StopReason: reason, Err: err, Partial: msg})
	}
}

// check drains a stream, requires it to satisfy the contract, and returns its
// events.
func check(t *testing.T, seq llm.StreamResponse) []llm.Event {
	t.Helper()
	events, err := llm.CheckStream(seq)
	if err != nil {
		t.Fatalf("CheckStream: %v", err)
	}
	return events
}

func types(events []llm.Event) []llm.EventType {
	out := make([]llm.EventType, len(events))
	for i, ev := range events {
		out[i] = ev.Type
	}
	return out
}

// toolCallIn is the one completed tool call in a stream, and fails the test if
// there is not exactly one.
func toolCallIn(t *testing.T, events []llm.Event) *llm.ToolCall {
	t.Helper()
	var found []*llm.ToolCall
	for _, ev := range events {
		if ev.Type == llm.EventToolCall {
			found = append(found, ev.ToolCall)
		}
	}
	if len(found) != 1 {
		t.Fatalf("stream reported %d completed tool calls, want 1", len(found))
	}
	return found[0]
}

func last(t *testing.T, events []llm.Event) llm.Event {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("stream yielded no events")
	}
	return events[len(events)-1]
}

// stream is a hand-written StreamResponse. Plain events rather than scripted,
// because the streams it builds are rules broken on purpose.
func stream(events ...llm.Event) llm.StreamResponse {
	return func(yield func(llm.Event) bool) {
		for _, ev := range events {
			if !yield(ev) {
				return
			}
		}
	}
}

func mustContain(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error; want one mentioning %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not mention %q", err, want)
	}
}
