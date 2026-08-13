package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// CheckStream drains seq, checks the StreamResponse contract on every event,
// and returns the events in order so a caller can go on to assert what the
// stream said rather than only that it was well formed. The error names the
// first violation and the event that committed it; the returned events stop
// there.
//
// The rules live here, once, because four packages have to satisfy them — the
// two adapters, the retry wrapper and the scripted fake — and every one of them
// is a place where "populate Partial" turns into "populate Partial except on
// the event I added last week". A test per adapter that re-derives the rules
// tests four different contracts.
//
// This is for tests: it consumes the stream, so nothing can read it afterwards,
// and against a real provider it blocks until the model has finished.
//
// One consequence of the contract surprises everyone once. Partial is a stable
// pointer into a message the provider mutates in place, so every event in the
// returned slice shows the FINAL message — not the message as it stood when
// that event was yielded. Anything that needs the intermediate state has to
// observe it during iteration, which is what the accumulation rules below do.
//
// One rule is deliberately not here: a provider must stop producing when yield
// returns false, and must not leave a goroutine behind when it does. CheckStream
// reads a stream to the end, so it never abandons one — and the useful half of
// that assertion is how much work the provider did after being abandoned, which
// only its own package can see. Each adapter tests that locally, with goleak in
// TestMain for the other half.
func CheckStream(seq StreamResponse) ([]Event, error) {
	if seq == nil {
		return nil, errors.New("nil StreamResponse; Stream always returns a sequence, and a " +
			"failure arrives as a terminal EventError inside it")
	}

	var (
		events  []Event
		checker streamChecker
	)

	for ev := range seq {
		events = append(events, ev)
		if err := checker.check(len(events)-1, ev); err != nil {
			return events, err
		}
	}

	switch {
	case len(events) == 0:
		return events, errors.New("stream yielded no events; a failure arrives as a terminal EventError, not as an empty sequence")
	case checker.terminal == "":
		return events, fmt.Errorf("stream ended after %d events without a terminal EventDone or EventError", len(events))
	}
	return events, checker.checkComplete()
}

// streamChecker carries what checking one event needs to know about the events
// before it: which message they pointed at, how much had accumulated in each
// channel, which calls it announced, and whether the stream was already over.
type streamChecker struct {
	partial   *Message // the pointer the first event carried
	seen      []string // per channel, what Partial held on the previous event
	announced map[string]bool

	terminal EventType  // the terminal event's type, once one has arrived
	stop     StopReason // and the reason it carried
}

// checkComplete runs once the stream has ended, on the rule that needs the
// whole of it: every tool_use block in the message was announced by an
// EventToolCall.
//
// This is the mirror of the direction checkToolCall covers, and the one design
// §4 invariant 1 states literally — a tool_use with no tool_result. An adapter
// that never flushes its last call leaves a block the loop runs no tool for, so
// no result answers it, and the next request is rejected. Nothing else would
// notice: a missing event is silence, and silence reads as a pass.
//
// It applies only to a message the provider claims is complete. A stream that
// failed or was truncated is expected to hold a half-built call — that is what
// design §4 invariant 2's guard exists to fail — so demanding an announcement
// there would reject exactly the streams the guard is written for.
func (c *streamChecker) checkComplete() error {
	if c.terminal != EventDone || (c.stop != StopEndTurn && c.stop != StopToolUse) {
		return nil
	}
	for _, block := range c.partial.Content {
		if block.Type == BlockToolUse && !c.announced[block.ID] {
			return fmt.Errorf("the message holds a tool_use block %q that no EventToolCall announced; "+
				"the loop dispatches from the event, so a call only in the message is one nothing runs "+
				"and nothing answers", block.ID)
		}
	}
	return nil
}

// channel is one of the three streams of content a message accumulates: the
// visible text, the thinking, and the JSON arguments of its tool calls. Each
// arrives in fragments, each has its own delta event, and each has to grow the
// same way — so they are one list rather than three copies of a rule.
//
// The third is the one that matters most, and it is the example internals §4.2
// opens with: `{"pa`, `th": "au`, `th.go"}`. Fragments of a JSON object split at
// arbitrary bytes, where losing accumulation does not garble a sentence, it
// hands the agent loop arguments that parse and mean something else.
type channel struct {
	name  string // how the error message says it
	kind  BlockType
	delta EventType
	text  func(Block) string
}

var channels = []channel{
	{"text", BlockText, EventTextDelta, func(b Block) string { return b.Text }},
	{"thinking", BlockThinking, EventThinkingDelta, func(b Block) string { return b.Text }},
	{"tool arguments", BlockToolUse, EventToolInputDelta, func(b Block) string { return string(b.Input) }},
}

func (c *streamChecker) check(index int, ev Event) error {
	at := fmt.Sprintf("event %d (%s)", index, ev.Type)

	if c.terminal != "" {
		return fmt.Errorf("%s arrived after the terminal %s event", at, c.terminal)
	}
	if ev.Partial == nil {
		return fmt.Errorf("%s has a nil Partial; every event carries the full accumulated message", at)
	}
	switch {
	case c.partial == nil:
		c.partial = ev.Partial
	case ev.Partial != c.partial:
		return fmt.Errorf("%s carries a different *Message than the events before it; "+
			"the message is allocated once, outside the stream loop, and mutated in place", at)
	}

	if (ev.ToolCall != nil) != (ev.Type == EventToolCall) {
		return fmt.Errorf("%s: ToolCall is set on EventToolCall and nothing else (here: %v)", at, ev.ToolCall != nil)
	}
	if (ev.Err != nil) != (ev.Type == EventError) {
		return fmt.Errorf("%s: Err is set on EventError and nothing else (here: %v)", at, ev.Err)
	}

	if c.seen == nil {
		c.seen = make([]string, len(channels))
	}
	for i, ch := range channels {
		if err := checkAccumulation(at, ev, ch, &c.seen[i]); err != nil {
			return err
		}
	}

	if ev.Type == EventToolCall {
		if err := checkToolCall(at, ev); err != nil {
			return err
		}
		if c.announced == nil {
			c.announced = map[string]bool{}
		}
		c.announced[ev.ToolCall.ID] = true
	}

	isTerminal := ev.Type == EventDone || ev.Type == EventError
	if isTerminal {
		c.terminal, c.stop = ev.Type, ev.StopReason
	}
	switch {
	case isTerminal && ev.StopReason == "":
		return fmt.Errorf("%s has no StopReason; the terminal event says why the model stopped", at)
	case !isTerminal && ev.StopReason != "":
		return fmt.Errorf("%s has StopReason %q; only the terminal event carries one, and a "+
			"consumer reading it per event would act on max_tokens mid-stream", at, ev.StopReason)
	case !isTerminal && ev.Partial.StopReason != "":
		return fmt.Errorf("%s: Partial.StopReason is %q before the stream ended; consumers are "+
			"told to read Partial, so a reason set early is the one they act on", at, ev.Partial.StopReason)
	case isTerminal && ev.Partial.StopReason != ev.StopReason:
		return fmt.Errorf("%s has StopReason %q but Partial.StopReason is %q; "+
			"the message is what gets persisted and what the retry classifier reads, "+
			"so it carries the reason too", at, ev.StopReason, ev.Partial.StopReason)
	}
	return nil
}

// checkToolCall holds what EventToolCall promises: arguments that are complete
// and parsed, for a call that also exists in the message. The second half is
// design §4 invariant 1 one step upstream — the loop runs the tool from the
// event and writes a tool_result, so a call missing from the message leaves a
// result answering nothing, and every provider rejects the next request.
func checkToolCall(at string, ev Event) error {
	call := ev.ToolCall

	switch {
	case call.ID == "":
		return fmt.Errorf("%s: ToolCall has no ID; the tool_result that answers it has nowhere to point", at)
	case call.Name == "":
		return fmt.Errorf("%s: ToolCall has no Name; nothing can be resolved from the registry", at)
	}
	called, err := arguments(call.Input)
	if err != nil {
		return fmt.Errorf("%s: ToolCall arguments %s; EventToolCall means they have arrived complete, "+
			"and a call taking none still sends {}", at, err)
	}

	for _, block := range ev.Partial.Content {
		if block.Type != BlockToolUse || block.ID != call.ID {
			continue
		}
		if block.Name != call.Name {
			return fmt.Errorf("%s: ToolCall %s names %q but the tool_use block with that id names %q",
				at, call.ID, call.Name, block.Name)
		}
		recorded, err := arguments(block.Input)
		if err != nil {
			return fmt.Errorf("%s: the tool_use block for %s holds arguments that %s; the message is "+
				"what gets persisted, so the fragments have to be finished there too", at, call.ID, err)
		}
		// By value, so whitespace and the order a provider serialized its keys
		// in do not count as disagreement.
		if !reflect.DeepEqual(recorded, called) {
			return fmt.Errorf("%s: ToolCall %s runs with %s but the tool_use block records %s; "+
				"the loop dispatches from the event and the message is what gets persisted, so a "+
				"transcript that disagrees describes something that never ran",
				at, call.ID, call.Input, block.Input)
		}
		return nil
	}
	return fmt.Errorf("%s: no tool_use block in Partial has id %q; the call has to exist in the "+
		"message as well as in the event", at, call.ID)
}

// arguments decodes a tool call's arguments, requiring a JSON object rather than
// merely valid JSON. The difference is load-bearing for `null`, which is valid
// JSON and which json.Unmarshal decodes into a struct as a silent no-op — so a
// tool would run with every argument zeroed instead of failing. An
// OpenAI-compatible endpoint normalising an empty arguments string is exactly
// how that arrives.
func arguments(raw json.RawMessage) (map[string]any, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("do not parse as JSON (%q)", raw)
	}
	if obj == nil {
		return nil, fmt.Errorf("are %q, not a JSON object", raw)
	}
	return obj, nil
}

// checkAccumulation holds the half of the contract that is easiest to satisfy
// on paper and get wrong in code: a delta event must leave Partial holding
// everything so far plus the delta, and no event may lose ground. seen is what
// the previous event held in this channel, and is advanced.
//
// The delta rule requires Delta to carry the fragment, which is the only thing
// a delta event has to say. The looser rule for every other event is what
// catches a channel being rewritten by an event that should not have touched it.
func checkAccumulation(at string, ev Event, ch channel, seen *string) error {
	got := ch.accumulated(ev.Partial)

	if ev.Type == ch.delta {
		if want := *seen + ev.Delta; got != want {
			return fmt.Errorf("%s: Partial %s is %q, want %q — Partial is the full accumulated message, not the delta",
				at, ch.name, got, want)
		}
	} else if !strings.HasPrefix(got, *seen) {
		return fmt.Errorf("%s: Partial %s is %q, which does not start with the %q already streamed; "+
			"accumulated content is never rewritten or dropped", at, ch.name, got, *seen)
	}

	*seen = got
	return nil
}

// accumulated joins this channel's content across every block of its kind,
// which is how much of it the message holds regardless of how the provider
// split it into blocks.
func (ch channel) accumulated(m *Message) string {
	var b strings.Builder
	for _, block := range m.Content {
		if block.Type == ch.kind {
			b.WriteString(ch.text(block))
		}
	}
	return b.String()
}
