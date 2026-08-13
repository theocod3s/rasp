package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// CheckStream drains seq, checks the StreamResponse contract on every event,
// and returns the events in order so a caller can go on to assert what the
// stream said rather than only that it was well formed. The error names the
// first violation and the event that committed it; the events are every event
// the stream yielded, violation or not.
//
// The rules live here, once, because three packages have to satisfy them — the
// two adapters and the scripted fake — and every one of them is a place where
// "populate Partial" turns into "populate Partial except on the event I added
// last week". A test per adapter that re-derives the rules tests three
// different contracts.
//
// A retry wrapper is NOT one of the three, and that is not an oversight. Design
// §12 makes "stream ended before message_stop" retryable, so a retry can happen
// after a first attempt has already streamed text and a terminal EventError —
// and a wrapper that simply plays attempt 2 after attempt 1 breaks three rules
// at once: an event after the terminal one, a second *Message, and content that
// was streamed and then dropped. Nothing here decides which way out it takes
// (buffer each attempt until it succeeds, and lose streaming on the first one;
// or reset the message and let the UI redraw). That belongs to the ticket that
// builds it, with the answer written down.
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
// One thing this cannot check, and it is worth knowing before trusting it: which
// block a fragment landed in. A delta event says what arrived and not where it
// belongs — Event carries no block index, because no consumer needs one, they
// render Partial — so an adapter that routes call 2's fragment into call 1's
// arguments passes every rule here as long as both halves still parse. That is
// internals §4.2's own hazard, arguments that parse and mean something else, and
// closing it means putting the wire's block index on delta events: a change to
// the event union, for the benefit of a check rather than a consumer, which is a
// decision for whoever writes the first adapter against real traffic.
//
// One rule is deliberately not here: a provider must stop producing when yield
// returns false, and must not leave a goroutine behind when it does. The useful
// half of that assertion is how much work the provider did after being
// abandoned, which only its own package can see. Each adapter tests that
// locally, with goleak in TestMain for the other half.
//
// Which is why this reads every stream to the end even after a violation. The
// alternative — return at the first one — would abandon the stream, so a
// provider that ignores yield's false return dies of the runtime's
// range-function panic instead of being told which rule it broke, and one that
// pumps events from a goroutine leaks it into an unrelated goleak failure. The
// diagnostic is the whole point of this function; it should not be the thing
// that gets lost.
func CheckStream(seq StreamResponse) ([]Event, error) {
	if seq == nil {
		return nil, errors.New("nil StreamResponse; Stream always returns a sequence, and a " +
			"failure arrives as a terminal EventError inside it")
	}

	var (
		events  []Event
		checker streamChecker
		first   error // the first violation, which is the one worth reporting
	)

	for ev := range seq {
		events = append(events, ev)
		if first != nil {
			continue
		}
		first = checker.check(len(events)-1, ev)
	}
	if first != nil {
		return events, first
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
	partial   *Message         // the pointer the first event carried
	seen      []map[int]string // per channel, what each block held on the previous event
	announced []string         // tool call ids, in the order EventToolCall reported them

	terminal EventType  // the terminal event's type, once one has arrived
	stop     StopReason // and the reason it carried
}

// Three sets, because "which terminal event may carry this reason" and "does
// this reason claim the message is finished" are different questions, and one
// list answering both is a list whose clauses go quietly unreachable.
//
// StopAborted appears in both terminal sets on purpose. A cancelled turn is an
// error to the code that was streaming and a completion to design §4's
// termination table, which commits what exists — so an adapter may end that
// stream either way, and neither reading is wrong enough to reject.
var (
	doneReasons  = []StopReason{StopEndTurn, StopToolUse, StopMaxTokens, StopRefusal, StopAborted}
	errorReasons = []StopReason{StopError, StopAborted}

	// finished are the reasons that claim a whole message arrived. Truncation
	// and cancellation are not among them: both stop mid-flight, and what they
	// leave behind is what design §4 invariant 2's guard exists to fail.
	finished = []StopReason{StopEndTurn, StopToolUse, StopRefusal}
)

// checkComplete runs once the stream has ended, on the rule that needs the whole
// of it: the tool calls the events announced are exactly the tool_use blocks the
// message holds, in the same order.
//
// This is the mirror of the direction checkToolCall covers, and the one design
// §4 invariant 1 states literally — a tool_use with no tool_result. An adapter
// that never flushes its last call leaves a block the loop runs no tool for, so
// no result answers it, and the next request is rejected. Nothing else would
// notice: a missing event is silence, and silence reads as a pass.
//
// Order counts for the same reason, one step further on. The loop dispatches
// from the events and lands results by index, so `tool_result` order follows
// announcement order while the persisted `tool_use` order follows the blocks
// (design §6 rule 6). An adapter that buffers its calls in a map and flushes at
// the end announces them in Go's randomized map order, and the symptom is the
// provider rejecting the *next* request — a long way from the cause.
//
// It applies only to a message the provider claims is complete. A stream that
// failed, was cancelled or ran out of output is expected to hold a half-built
// call — that is what design §4 invariant 2's guard exists to fail — so
// demanding an announcement there would reject exactly the streams the guard is
// written for.
func (c *streamChecker) checkComplete() error {
	if c.terminal != EventDone || !slices.Contains(finished, c.stop) {
		return nil
	}

	var recorded []string
	for _, block := range c.partial.Content {
		if block.Type == BlockToolUse {
			recorded = append(recorded, block.ID)
		}
	}

	for i, id := range recorded {
		if slices.Index(recorded, id) != i {
			return fmt.Errorf("the message holds two tool_use blocks with id %q; one tool_result "+
				"would answer both, so the pairing that design §4 invariant 1 rests on stops being "+
				"a pairing at all", id)
		}
	}

	for _, id := range recorded {
		if !slices.Contains(c.announced, id) {
			return fmt.Errorf("the message holds a tool_use block %q that no EventToolCall announced; "+
				"the loop dispatches from the event, so a call only in the message is one nothing runs "+
				"and nothing answers", id)
		}
	}
	if !slices.Equal(recorded, c.announced) {
		return fmt.Errorf("the message records tool calls %v but the stream announced %v; results are "+
			"answered in announcement order and persisted in block order, so any difference in order "+
			"or in count leaves tool_result blocks that do not line up", recorded, c.announced)
	}

	// The reason and the call count have to agree. A turn that asks for tools
	// says so, and one that says so has something to dispatch: design §4's
	// termination table has no row for tool_use with nothing to run, and the
	// loop would spend a step on an empty dispatch and go round again.
	if (len(recorded) > 0) != (c.stop == StopToolUse) {
		return fmt.Errorf("the stream ended with StopReason %q holding %d tool calls; %q is the reason "+
			"for a turn with calls, and the only reason for one", c.stop, len(recorded), StopToolUse)
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
	name string // how the error message says it
	kind BlockType

	// delta is the event that adds a fragment to this channel, and the only one
	// whose Delta has to match what arrived exactly. grows lists the other
	// events allowed to add to it at all.
	delta EventType
	grows []EventType

	// placeholder is a value that counts as a fresh start rather than as
	// content: the next fragment replaces it instead of extending it. Empty
	// means the channel has none.
	placeholder string

	text func(Block) string
}

var channels = []channel{
	{name: "text", kind: BlockText, delta: EventTextDelta,
		text: func(b Block) string { return b.Text }},
	{name: "thinking", kind: BlockThinking, delta: EventThinkingDelta,
		text: func(b Block) string { return b.Text }},
	// Tool arguments carry two allowances past their delta, and both are real
	// wire shapes rather than latitude. Anthropic's content_block_start for a
	// tool_use carries `"input": {}` — for every tool, not only the ones taking
	// no arguments — and the fragments follow it; an OpenAI-compatible endpoint
	// may instead not reveal `arguments` until its final chunk, so the whole
	// payload can arrive at the completed call. Tightening either one back
	// breaks an adapter, which is why they are written down here rather than
	// discovered from an error message.
	//
	// The empty object is why placeholder exists: an adapter that copies that
	// field faithfully starts at `{}` and then accumulates `{"pa` over it, which
	// is a rewrite of the bytes and an addition to the arguments. The allowance
	// is on what was there before, not on the reader — arguments that genuinely
	// ARE `{}` still have to arrive like any other payload, and a fragment
	// appended to the placeholder is still wrong.
	{name: "tool arguments", kind: BlockToolUse, delta: EventToolInputDelta,
		grows:       []EventType{EventToolInputStart, EventToolCall},
		placeholder: "{}",
		text:        func(b Block) string { return string(b.Input) }},
}

// carriesDelta reports whether this event type is some channel's delta, which is
// the only kind of event with anything to put in Delta.
func carriesDelta(t EventType) bool {
	for _, ch := range channels {
		if ch.delta == t {
			return true
		}
	}
	return false
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

	// On every event, not only the first. The pointer is stable, so an adapter
	// that overwrites the message wholesale when it maps the final chunk keeps
	// the address and loses the role — and the rule that watches the address
	// cannot see that.
	if ev.Partial.Role != RoleAssistant {
		return fmt.Errorf("%s: Partial.Role is %q, want %q; the message is allocated before the "+
			"first event and persisted after the last, and a transcript holding a message with no "+
			"role is rejected the next time it is replayed", at, ev.Partial.Role, RoleAssistant)
	}

	if (ev.ToolCall != nil) != (ev.Type == EventToolCall) {
		return fmt.Errorf("%s: ToolCall is set on EventToolCall and nothing else (here: %v)", at, ev.ToolCall != nil)
	}
	if (ev.Err != nil) != (ev.Type == EventError) {
		return fmt.Errorf("%s: Err is set on EventError and nothing else (here: %v)", at, ev.Err)
	}
	if ev.Delta != "" && !carriesDelta(ev.Type) {
		return fmt.Errorf("%s carries Delta %q; only a delta event has newly-arrived content, and a "+
			"consumer that appends whatever Delta holds would render this twice", at, ev.Delta)
	}

	if c.seen == nil {
		c.seen = make([]map[int]string, len(channels))
		for i := range c.seen {
			c.seen[i] = map[int]string{}
		}
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
		c.announced = append(c.announced, ev.ToolCall.ID)
	}

	isTerminal := ev.Type == EventDone || ev.Type == EventError
	if isTerminal {
		c.terminal, c.stop = ev.Type, ev.StopReason
	}
	switch {
	case !isTerminal && ev.StopReason != "":
		return fmt.Errorf("%s has StopReason %q; only the terminal event carries one, and a "+
			"consumer reading it per event would act on max_tokens mid-stream", at, ev.StopReason)
	case !isTerminal && ev.Partial.StopReason != "":
		return fmt.Errorf("%s: Partial.StopReason is %q before the stream ended; consumers are "+
			"told to read Partial, so a reason set early is the one they act on", at, ev.Partial.StopReason)
	case isTerminal:
		return checkStopReason(at, ev)
	}
	return nil
}

// checkStopReason holds the terminal event's own rules. The loop branches on the
// stop reason (design §4's termination table) and the retry classifier reads it
// off the Message, so it has to be present, has to agree with the message, and
// has to agree with the event type: an EventError reporting end_turn is a
// truncated reply the loop would present as complete and never retry, and an
// EventDone reporting error retries a turn that finished.
func checkStopReason(at string, ev Event) error {
	want := doneReasons
	if ev.Type == EventError {
		want = errorReasons
	}

	switch {
	case ev.StopReason == "":
		return fmt.Errorf("%s has no StopReason; the terminal event says why the model stopped", at)
	case !slices.Contains(want, ev.StopReason):
		return fmt.Errorf("%s carries StopReason %q; %s carries one of %v", at, ev.StopReason, ev.Type, want)
	case ev.Partial.StopReason != ev.StopReason:
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
		// Byte-identical, not merely equal by value. The looser rule would
		// contradict the accumulation rule, which compares the fragments as
		// they arrived — and the message is meant to hold what the model sent,
		// not our re-rendering of it.
		if !bytes.Equal(block.Input, call.Input) {
			if reflect.DeepEqual(recorded, called) {
				return fmt.Errorf("%s: ToolCall %s and its tool_use block hold the same arguments "+
					"serialized differently (%s against %s); keep the fragments verbatim on both "+
					"sides, so the transcript is what the model actually sent",
					at, call.ID, call.Input, block.Input)
			}
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
// The two failures are reported apart because they send an adapter author to
// different places: unfinished fragments are an accumulation bug, and a JSON
// array or number is a mapping bug.
func arguments(raw json.RawMessage) (map[string]any, error) {
	if !json.Valid(raw) {
		return nil, fmt.Errorf("do not parse as JSON (%q)", raw)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("are %q, not a JSON object", raw)
	}
	return obj, nil
}

// checkAccumulation holds the half of the contract that is easiest to satisfy on
// paper and get wrong in code: a delta event must leave Partial holding
// everything so far plus the delta, and no event may lose ground.
//
// It works per block, not on the channel's blocks joined together, and that is
// not a detail. Two parallel tool calls can stream interleaved — the
// OpenAI-compatible wire shape indexes fragments by call — so a joined string
// would see call 2's first fragment land in the middle of call 1's arguments and
// call it a rewrite. Rejecting a conformant adapter is worse than missing a bug,
// because the adapter author has no way to tell which they are looking at.
//
// The delta rule requires Delta to carry the fragment, which is the only thing a
// delta event has to say, and to add it to exactly one block. Every other event
// must leave the channel exactly as it was — which catches content being
// rewritten, and equally catches it *growing* where no event announced the
// growth: content nobody was told about is content a UI keyed on event types
// never draws.
//
// The exception is a channel's grows list, and it is wider than "one last
// fragment": those events may deliver the entire payload, with no delta before
// them and nothing in Delta. That is what a no-argument tool call looks like on
// one provider and what a late-arriving arguments field looks like on another,
// so the latitude is deliberate and only tool arguments have it.
func checkAccumulation(at string, ev Event, ch channel, seen *map[int]string) error {
	now := ch.snapshot(ev.Partial)

	// Sorted, so a message naming a block index names the same one on every run.
	// The error string is this function's whole product; a flaky one is a flaky
	// test in the package that exists to be depended on.
	for _, i := range slices.Sorted(maps.Keys(*seen)) {
		if _, ok := now[i]; !ok {
			return fmt.Errorf("%s: the %s block at index %d is gone; blocks are only ever added to, "+
				"and a message that loses one has lost part of the reply", at, ch.name, i)
		}
	}

	grew, added := 0, ""
	for _, i := range slices.Sorted(maps.Keys(now)) {
		text, was := now[i], (*seen)[i]
		switch {
		case text == was:
			// Unchanged, whatever it holds — including a placeholder that stays
			// one because the call takes no arguments.
		case was == ch.placeholder && ch.placeholder != "":
			grew++
			if grew == 1 {
				added = text
			}
		case !strings.HasPrefix(text, was):
			return fmt.Errorf("%s: Partial %s at index %d is %q, which does not start with the %q "+
				"already streamed; accumulated content is never rewritten or dropped", at, ch.name, i, text, was)
		default:
			grew++
			if grew == 1 {
				added = text[len(was):]
			}
		}
	}

	switch {
	case ev.Type == ch.delta:
		switch {
		case grew > 1:
			return fmt.Errorf("%s: Partial %s grew in %d blocks at once; one delta adds to one block",
				at, ch.name, grew)
		case added != ev.Delta:
			return fmt.Errorf("%s: Partial %s grew by %q, want %q — Partial is the full accumulated "+
				"message, not the delta", at, ch.name, added, ev.Delta)
		}
	case slices.Contains(ch.grows, ev.Type):
		// Any amount, in any block: see the latitude above.
	case grew > 0:
		return fmt.Errorf("%s: Partial %s grew by %q; only %v adds to it, and content nobody "+
			"announced is content nobody draws", at, ch.name, added, append([]EventType{ch.delta}, ch.grows...))
	}

	*seen = now
	return nil
}

// snapshot is this channel's content per block, keyed by the block's index in
// the message. The index is the identity: it is what the wire formats use to
// route fragments, and it is stable because blocks are only ever appended.
func (ch channel) snapshot(m *Message) map[int]string {
	out := map[int]string{}
	for i, block := range m.Content {
		if block.Type == ch.kind {
			out[i] = ch.text(block)
		}
	}
	return out
}
