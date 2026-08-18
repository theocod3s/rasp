package fake

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/theocod3s/rasp/internal/llm"
)

// Step is one scripted piece of a turn. Build one with MessageStart, Text,
// Thinking, ToolCall, UnfinishedToolCall, Usage, Hook, Done or Fail; the zero
// value is not a step and playing one panics.
type Step struct {
	kind   kind
	chunks []string
	name   string
	id     string // stamped by New
	usage  llm.Usage
	reason llm.StopReason
	err    error
	fn     func()
}

// kind starts at invalid so a zero Step is caught rather than silently played as
// whichever constructor happens to sit first.
type kind int

const (
	kindInvalid kind = iota
	kindMessageStart
	kindText
	kindThinking
	kindToolCall
	kindUnfinishedToolCall
	kindUsage
	kindHook
	kindDone
	kindFail
)

// MessageStart opens the turn with the optional EventMessageStart, and has to be
// the turn's first step.
//
// Scripted rather than emitted by default, because the event is one an
// OpenAI-compatible stream has no equivalent for: a fake that always sent it
// would let a consumer key its setup on the event and pass every test here
// before failing against half the adapters.
func MessageStart() Step { return Step{kind: kindMessageStart} }

// Text streams one text block. Several chunks make several delta events out of
// one block, the way a provider splits a sentence at boundaries of its own; no
// chunks make the block that opens and never receives a delta, which a finished
// turn may legally carry (decisions.md).
func Text(chunks ...string) Step { return Step{kind: kindText, chunks: chunks} }

// Thinking streams one thinking block, chunk by chunk as Text does.
func Thinking(chunks ...string) Step { return Step{kind: kindThinking, chunks: chunks} }

// ToolCall streams one complete call: the block, its arguments as fragments that
// need not individually parse, then the EventToolCall the loop dispatches from.
//
// Arguments default to `{}` when no fragment is given, which is what a call to a
// tool taking none sends.
func ToolCall(name string, fragments ...string) Step {
	if len(fragments) == 0 {
		fragments = []string{"{}"}
	}
	return Step{kind: kindToolCall, name: name, chunks: fragments}
}

// UnfinishedToolCall streams a call whose arguments stop mid-flight: no
// EventToolCall, and a tool_use block holding a fragment. Pair it with
// Done(llm.StopMaxTokens) for the shape design §4 invariant 2 exists to fail,
// where truncated arguments can still parse and mean something else.
//
// Unlike ToolCall, no fragments means exactly that: a call cut off before any
// arguments arrived at all.
func UnfinishedToolCall(name string, fragments ...string) Step {
	return Step{kind: kindUnfinishedToolCall, name: name, chunks: fragments}
}

// Usage sets what the turn reports it spent. It yields no event of its own —
// counts ride along on whatever event comes next, as they do on the wire.
func Usage(u llm.Usage) Step { return Step{kind: kindUsage, usage: u} }

// Hook runs fn at this point in the stream, and is the deterministic way to
// cancel a turn mid-flight: the next event is then the terminal EventError
// carrying llm.StopAborted, with no timing to lose a race to. It runs when a
// turn is played and not when New validates the script.
func Hook(fn func()) Step { return Step{kind: kindHook, fn: fn} }

// Done ends the turn with EventDone and reason.
func Done(reason llm.StopReason) Step { return Step{kind: kindDone, reason: reason} }

// Fail ends the turn with a terminal EventError carrying err — the only shape a
// scripted failure has, since Stream never returns an error (design §3.1).
func Fail(err error) Step { return Step{kind: kindFail, err: err} }

const (
	withHooks    = true
	withoutHooks = false
)

func play(ctx context.Context, req llm.Request, steps []Step, hooks bool) llm.StreamResponse {
	return func(yield func(llm.Event) bool) {
		// The message is allocated here, before anything can yield: Partial is this
		// pointer on every event, and allocating inside the loop meets the letter
		// of the contract while throwing away the reason it is affordable.
		e := &emitter{
			ctx:   ctx,
			yield: yield,
			hooks: hooks,
			msg:   &llm.Message{Role: llm.RoleAssistant, Model: req.Model, Provider: ProviderID},
		}
		for _, step := range steps {
			if !step.play(e) {
				return
			}
		}
	}
}

func (s Step) play(e *emitter) bool {
	switch s.kind {
	case kindMessageStart:
		return e.live() && e.send(llm.Event{Type: llm.EventMessageStart})
	case kindText:
		return e.deltas(llm.BlockText, llm.EventTextDelta, s.chunks)
	case kindThinking:
		return e.deltas(llm.BlockThinking, llm.EventThinkingDelta, s.chunks)
	case kindToolCall:
		return e.toolCall(s, announce)
	case kindUnfinishedToolCall:
		return e.toolCall(s, leaveUnfinished)
	case kindUsage:
		e.msg.Usage = s.usage
		return true
	case kindHook:
		if e.hooks && s.fn != nil {
			s.fn()
		}
		return true
	case kindDone:
		return e.live() && e.terminal(llm.EventDone, s.reason, nil)
	case kindFail:
		return e.live() && e.terminal(llm.EventError, llm.StopError, s.err)
	}
	panic(fmt.Sprintf("fake: step kind %d is not one this package can play; build steps with "+
		"Text, ToolCall, Done and the rest, never as a Step literal", s.kind))
}

// emitter accumulates the turn's message and yields the events that announce
// each change to it.
type emitter struct {
	ctx   context.Context
	yield func(llm.Event) bool
	msg   *llm.Message
	hooks bool
}

// send returns false once the consumer has stopped listening, and every caller
// has to carry that answer out of the stream rather than finish the turn off:
// yielding again panics the iterator runtime.
func (e *emitter) send(ev llm.Event) bool {
	ev.Partial = e.msg
	return e.yield(ev)
}

// live reports whether another event may be produced, and ends the turn when the
// answer is that the context was cancelled — out through the terminal EventError,
// the same door as any other runtime failure.
//
// Every caller asks BEFORE mutating the message rather than after, so a cancelled
// turn commits no block that never streamed.
func (e *emitter) live() bool {
	if err := e.ctx.Err(); err != nil {
		e.terminal(llm.EventError, llm.StopAborted, err)
		return false
	}
	return true
}

func (e *emitter) terminal(t llm.EventType, reason llm.StopReason, err error) bool {
	e.msg.StopReason = reason
	return e.send(llm.Event{Type: t, StopReason: reason, Err: err})
}

func (e *emitter) deltas(kind llm.BlockType, event llm.EventType, chunks []string) bool {
	if !e.live() {
		return false
	}
	e.msg.Content = append(e.msg.Content, llm.Block{Type: kind})
	at := len(e.msg.Content) - 1

	for _, chunk := range chunks {
		if !e.live() {
			return false
		}
		e.msg.Content[at].Text += chunk
		if !e.send(llm.Event{Type: event, Delta: chunk}) {
			return false
		}
	}
	return true
}

const (
	announce        = true
	leaveUnfinished = false
)

func (e *emitter) toolCall(s Step, complete bool) bool {
	if !e.live() {
		return false
	}
	e.msg.Content = append(e.msg.Content, llm.Block{Type: llm.BlockToolUse, ID: s.id, Name: s.name})
	at := len(e.msg.Content) - 1
	if !e.send(llm.Event{Type: llm.EventToolInputStart}) {
		return false
	}

	var input string
	for _, frag := range s.chunks {
		if !e.live() {
			return false
		}
		input += frag
		e.msg.Content[at].Input = json.RawMessage(input)
		if !e.send(llm.Event{Type: llm.EventToolInputDelta, Delta: frag}) {
			return false
		}
	}

	if !complete {
		return true
	}
	if !e.live() {
		return false
	}
	// A fresh ToolCall value, never a pointer reused between announcements: the
	// loop buffers every call before it runs any, so one shared pointer would
	// leave it holding N views of the last.
	return e.send(llm.Event{
		Type:     llm.EventToolCall,
		ToolCall: &llm.ToolCall{ID: s.id, Name: s.name, Input: json.RawMessage(input)},
	})
}
