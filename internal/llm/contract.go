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

// CheckStream drains seq, checks the StreamResponse contract on every event, and
// returns the events in order so a caller can go on to assert what the stream
// said. For tests: it consumes the stream, and against a real provider it blocks
// until the model has finished.
//
// Partial is a stable pointer, so every event in the returned slice shows the
// FINAL message rather than the message as it stood when that event was yielded.
//
// Four things go deliberately unchecked:
//
// Which block a fragment landed in — Event carries no block index, so an adapter
// routing call 2's fragment into call 1's arguments passes as long as both halves
// parse. Adding one for a check's benefit rather than a consumer's is the first
// real-traffic adapter's call. The same blindness is why arguments that genuinely
// are `{}` may be replaced wholesale: telling that apart from a provider's
// opening placeholder was tried and reverted for rejecting two real wire shapes.
//
// How many blocks an event completing a call may add to — one chunk from an
// OpenAI-compatible endpoint can carry two entries, and a finished turn catches
// the mis-routing anyway, byte for byte.
//
// Token usage, because requiring it would reject an endpoint that reports none,
// and design §10.2 degrades to estimates rather than refusing.
//
// That a provider stops producing when yield returns false — the useful half is
// how much work it did after being abandoned, which only its own package sees.
// That is also why this reads every stream to the end even after a violation:
// abandoning it makes a provider that ignores yield die of the runtime's
// range-function panic instead of being told which rule it broke.
//
// A retry wrapper cannot satisfy this by replaying attempt 2 after attempt 1 —
// that is an event after the terminal one, a second *Message, and content
// streamed then dropped, all at once. The way out belongs to the ticket that
// builds it.
func CheckStream(seq StreamResponse) ([]Event, error) {
	if seq == nil {
		return nil, errors.New("nil StreamResponse; Stream always returns a sequence, and a " +
			"failure arrives as a terminal EventError inside it")
	}

	var (
		events  []Event
		checker streamChecker
		first   error // the first violation is the one worth reporting
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
	if err := checker.checkSettled(); err != nil {
		return events, err
	}
	return events, checker.checkComplete()
}

// streamChecker carries what checking one event needs to know about the events
// before it.
type streamChecker struct {
	partial   *Message          // the pointer the first event carried
	seen      []map[int]string  // per channel, what each block held on the previous event
	announced []string          // tool call ids, in the order EventToolCall reported them
	frozen    map[int]announced // blocks whose call was announced, and what it was announced as

	terminal EventType  // the terminal event's type, once one has arrived
	stop     StopReason // and the reason it carried
}

// Three sets, because "which terminal event may carry this reason" and "does
// this reason claim the message is finished" are different questions.
//
// StopAborted is in both terminal sets on purpose: a cancelled turn is an error
// to the code that was streaming and a completion to design §4's termination
// table, so an adapter may end that stream either way.
var (
	doneReasons  = []StopReason{StopEndTurn, StopToolUse, StopMaxTokens, StopRefusal, StopAborted}
	errorReasons = []StopReason{StopError, StopAborted}

	// finished are the reasons claiming every tool call was announced, and they
	// gate the emptiness rules below. Truncation and cancellation stop mid-flight,
	// leaving what design §4 invariant 2's guard exists to fail.
	//
	// Refusal's absence reads oddly and is load-bearing both ways. A model can
	// decline part way through a call, which design §4's termination table does
	// not cover, and can decline before producing anything, which arrives as a
	// 200 with no blocks. Requiring announcements or content from a refusal
	// leaves a faithful adapter nowhere to put those, since errorReasons does not
	// take StopRefusal either. Refusing to *commit* an empty message is the
	// commit point's rule.
	finished = []StopReason{StopEndTurn, StopToolUse}
)

// checkComplete runs once the stream has ended, on the rule that needs the whole
// of it: the calls the events announced are exactly the tool_use blocks the
// message holds, in the same order. Nothing else would notice a breach, because
// a missing event is silence and silence reads as a pass — and the order half
// catches an adapter that buffers calls in a map and announces them in Go's
// randomized order, whose symptom is the provider rejecting the *next* request.
func (c *streamChecker) checkComplete() error {
	var recorded []string
	for index, block := range c.partial.Content {
		if block.Type != BlockToolUse {
			continue
		}
		// checkToolCall only vets the event's copy, so a turn that broke off could
		// otherwise commit a tool_use with no id and poison Sanitize.
		switch {
		case block.ID == "":
			return fmt.Errorf("the tool_use block at index %d has no id; the tool_result that answers "+
				"it has nowhere to point, and design §4 invariant 1 rests on that pairing", index)
		case block.Name == "":
			return fmt.Errorf("the tool_use block %q has no name; nothing can be resolved from the "+
				"registry to run it", block.ID)
		}
		recorded = append(recorded, block.ID)
	}

	// Uniqueness holds however the stream ended: a truncated turn writes a
	// tool_result per pending call too (design §4 invariant 2).
	for i, id := range recorded {
		if slices.Index(recorded, id) != i {
			return fmt.Errorf("the message holds two tool_use blocks with id %q; one tool_result "+
				"would answer both, so the pairing that design §4 invariant 1 rests on stops being "+
				"a pairing at all", id)
		}
	}

	// Order too, however the stream ended. What a half-finished turn cannot be
	// held to is announcing *everything*.
	for i, id := range c.announced {
		if slices.Index(c.announced, id) != i {
			return fmt.Errorf("the stream announced tool call %q twice; the loop dispatches once per "+
				"announcement, so the second one runs a tool the model asked for once", id)
		}
	}
	if !inOrder(c.announced, recorded) {
		return fmt.Errorf("the stream announced tool calls %v, which is not the order the message "+
			"records them in (%v); results are answered in announcement order and persisted in block "+
			"order, so the two have to agree", c.announced, recorded)
	}

	// With the rules above this completes "the announcements are exactly the
	// blocks": a subsequence containing every element, of a list with no repeats,
	// is the list.
	//
	// A turn that broke off is exempt, and the rule was tried the other way round.
	// The connection can drop after the last argument fragment and before the
	// event confirming the call, so requiring an announcement leaves an adapter no
	// way through but to announce it anyway — and then the loop dispatches a call
	// nobody confirmed, which for `write` means a file changed on the strength of
	// a dropped connection.
	if c.terminal == EventDone && slices.Contains(finished, c.stop) {
		for index, block := range c.partial.Content {
			if block.Type != BlockToolUse {
				continue
			}
			if !slices.Contains(c.announced, block.ID) {
				return fmt.Errorf("the message holds a tool_use block %q that no EventToolCall "+
					"announced; the loop dispatches from the event, so a call only in the message is "+
					"one nothing runs and nothing answers (block %d)", block.ID, index)
			}
		}
	}

	if c.terminal != EventDone || !slices.Contains(finished, c.stop) {
		return nil
	}

	// One direction only: Ollama and llama.cpp-style servers report finish_reason
	// "stop" alongside tool_calls, and the loop dispatches on the calls anyway.
	if c.stop == StopToolUse && len(recorded) == 0 {
		return fmt.Errorf("the stream ended with StopReason %q and no tool calls; that reason says the "+
			"model stopped to use one", c.stop)
	}

	// Design §4 step 6 commits whatever the turn produced without asking, so this
	// is the only place to catch an empty one while the blame is the adapter's.
	if len(c.partial.Content) == 0 {
		return fmt.Errorf("the stream ended with StopReason %q and no content; a finished message with "+
			"no blocks in it is refused by every provider the next time it is sent", c.stop)
	}
	for index, block := range c.partial.Content {
		if (block.Type == BlockText || block.Type == BlockThinking) && block.Text == "" {
			return fmt.Errorf("the stream ended with StopReason %q and an empty %s block at index %d; "+
				"a block with nothing in it is refused on replay exactly like a message with no blocks, "+
				"and a turn that finished had time to fill it", c.stop, block.Type, index)
		}
	}
	return nil
}

// inOrder reports whether announced is a subsequence of recorded.
func inOrder(announced, recorded []string) bool {
	at := 0
	for _, id := range announced {
		next := slices.Index(recorded[at:], id)
		if next < 0 {
			return false
		}
		at += next + 1
	}
	return true
}

// channel is one of the three streams of content a message accumulates: text,
// thinking, and tool arguments. Each grows the same way, so they are one list
// rather than three copies of a rule.
//
// The third matters most — internals §4.2's example is `{"pa`, `th": "au`,
// `th.go"}`, where losing accumulation does not garble a sentence, it hands the
// agent loop arguments that parse and mean something else.
type channel struct {
	name string // how the error message says it
	kind BlockType

	// delta is the only event whose Delta has to match what arrived exactly.
	// grows lists the others allowed to add to the channel at all.
	delta EventType
	grows []EventType

	// placeholder counts as a fresh start rather than as content: the next
	// fragment replaces it instead of extending it. Empty means there is none.
	placeholder string

	text func(Block) string
}

var channels = []channel{
	{name: "text", kind: BlockText, delta: EventTextDelta,
		text: func(b Block) string { return b.Text }},
	{name: "thinking", kind: BlockThinking, delta: EventThinkingDelta,
		text: func(b Block) string { return b.Text }},
	// Two allowances past the delta, both real wire shapes: Anthropic's
	// content_block_start carries `"input": {}` for every tool_use with the
	// fragments following it, while an OpenAI-compatible endpoint may not reveal
	// `arguments` until its final chunk. Tightening either back breaks an adapter.
	//
	// The empty object is why placeholder exists, and the allowance is on what was
	// there before rather than on what arrives: arguments that genuinely ARE `{}`
	// still have to arrive like any other payload.
	{name: "tool arguments", kind: BlockToolUse, delta: EventToolInputDelta,
		grows:       []EventType{EventToolInputStart, EventToolCall},
		placeholder: emptyArguments,
		text:        func(b Block) string { return string(b.Input) }},
}

// emptyArguments is what a provider puts in a tool block's input before any
// fragment arrives, and equally what a call taking no arguments ends up with.
// That the two are the same string is the ambiguity several rules here work
// around.
const emptyArguments = "{}"

// knownEvents is every event type this contract recognises. Anything else is
// rejected: a consumer's type switch ignores it silently, and every other rule
// here keys off what an event holds, so a mistyped one carrying no data would
// slip past all of them.
var knownEvents = []EventType{
	EventMessageStart,
	EventTextDelta,
	EventThinkingDelta,
	EventToolInputStart,
	EventToolInputDelta,
	EventToolCall,
	EventDone,
	EventError,
}

// carriesDelta reports whether this event type is some channel's delta, the only
// kind with anything to put in Delta.
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
	if !slices.Contains(knownEvents, ev.Type) {
		return fmt.Errorf("%s is not an event type this contract knows; a consumer switches on the "+
			"type and ignores what it does not recognise, so this arrives as nothing at all", at)
	}

	// A stream is one message, and message_start opens it. A consumer that resets
	// per-message state on this event would wipe a half-drawn reply.
	if ev.Type == EventMessageStart && index > 0 {
		return fmt.Errorf("%s: message_start arrived after %d other events; it is the event that opens "+
			"a stream, and a stream carries one message", at, index)
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

	// On every event, not only the first: an adapter that overwrites the message
	// wholesale when it maps the final chunk keeps the address and loses the
	// role, which the rule watching the address cannot see.
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
	// An empty Delta on a delta event is allowed on purpose: Anthropic opens a
	// tool block's fragment stream with an empty partial_json, so an adapter
	// forwarding its events one-for-one would fail its first tool call.
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
		if err := checkAccumulation(at, ev, ch, c.seen[i], c.frozen); err != nil {
			return err
		}
	}
	if err := c.checkFrozen(at, ev.Partial); err != nil {
		return err
	}
	if err := checkShape(at, ev.Partial); err != nil {
		return err
	}

	if ev.Type == EventToolCall {
		index, err := checkToolCall(at, ev)
		if err != nil {
			return err
		}
		c.announced = append(c.announced, ev.ToolCall.ID)
		if c.frozen == nil {
			c.frozen = map[int]announced{}
		}
		// Nothing may touch that block again: the loop dispatches from this
		// event, so call 2's tail mis-routed onto call 1 would change what the
		// transcript says was run, after it ran.
		c.frozen[index] = announced{
			call: ev.ToolCall,
			id:   ev.ToolCall.ID,
			name: ev.ToolCall.Name,
			// Cloned: comparing the header would not notice an adapter appending
			// into the same array.
			input: bytes.Clone(ev.ToolCall.Input),
		}
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

// checkStopReason holds the terminal event's own rules: the reason has to be
// present, and to agree with both the message and the event type. An EventError
// reporting end_turn is a truncated reply the loop presents as complete and
// never retries; an EventDone reporting error retries a turn that finished.
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

// checkSettled re-reads the message once the stream has ended. The agent loop
// commits Partial after iteration finishes, so a provider touching the message
// while unwinding has a real window that nothing inside the loop sees.
func (c *streamChecker) checkSettled() error {
	for i, ch := range channels {
		settled := ch.snapshot(c.partial)

		// Both directions. An append is the more tempting bug: it looks like
		// finishing the message off, and no event announced it.
		for _, index := range slices.Sorted(maps.Keys(settled)) {
			if _, ok := c.seen[i][index]; !ok {
				return fmt.Errorf("a %s block appeared at index %d after the stream ended; the message "+
					"is committed after the last event, so content added here was announced by nothing",
					ch.name, index)
			}
		}
		for _, index := range slices.Sorted(maps.Keys(c.seen[i])) {
			was, now := c.seen[i][index], settled[index]
			if _, ok := settled[index]; !ok {
				return fmt.Errorf("the %s block at index %d is gone after the stream ended; the message "+
					"is committed after the last event, so what is missing here is missing from the "+
					"transcript", ch.name, index)
			}
			if was != now {
				return fmt.Errorf("Partial %s at index %d changed after the stream ended, from %q to "+
					"%q; the message is committed after the last event, so anything touched here is "+
					"content no event announced", ch.name, index, was, now)
			}
		}
	}

	// The two fields read after the fact: session storage needs the role, the
	// retry classifier needs the stop reason. Clearing either while unwinding
	// leaves a transcript rejected on replay, or a failure nothing retries.
	if c.partial.Role != RoleAssistant {
		return fmt.Errorf("Partial.Role is %q after the stream ended, want %q", c.partial.Role, RoleAssistant)
	}
	if c.partial.StopReason != c.stop {
		return fmt.Errorf("Partial.StopReason is %q after the stream ended; the terminal event said %q",
			c.partial.StopReason, c.stop)
	}

	// checkFrozen and checkShape run per event, so they stop looking at exactly
	// the point the loop starts reading.
	if err := checkShape("after the stream ended", c.partial); err != nil {
		return err
	}
	return c.checkFrozen("after the stream ended", c.partial)
}

// checkShape catches what the per-channel rules cannot see: a block of a type no
// channel watches. A tool_result in a streamed message is a 400 on the next
// request; an unrecognised block is content nothing above the provider can draw.
func checkShape(at string, msg *Message) error {
	for index, block := range msg.Content {
		switch block.Type {
		case BlockText, BlockThinking, BlockToolUse:
		default:
			return fmt.Errorf("%s: the block at index %d is a %q; a stream produces an assistant "+
				"message, which holds text, thinking and tool_use blocks and nothing else",
				at, index, block.Type)
		}
	}
	return nil
}

// announced is one confirmed call: the event's pointer, and a copy of what it
// said at the time. Both halves, because the block can drift from the call, and
// the call from itself when an adapter reuses one *ToolCall the way it is told
// to reuse one *Message.
type announced struct {
	call     *ToolCall
	id, name string
	input    []byte
}

// checkFrozen re-reads the blocks whose calls have been announced. The
// accumulation rules watch their arguments; this watches what the call IS, since
// renaming read to write after the loop ran read rewrites the transcript's
// account of something that already happened.
func (c *streamChecker) checkFrozen(at string, msg *Message) error {
	// Walking the blocks rather than the frozen map: same order, and a block that
	// left the message is already caught by the content rules.
	for index, block := range msg.Content {
		was, ok := c.frozen[index]
		if !ok {
			continue
		}
		switch {
		case block.Name != was.name:
			return fmt.Errorf("%s: the block at index %d was announced as a call to %q and now names "+
				"%q; the loop dispatched the first one", at, index, was.name, block.Name)
		case block.ID != was.id:
			return fmt.Errorf("%s: the block at index %d was announced as call %q and now carries id "+
				"%q; the result the loop writes will name the first one", at, index, was.id, block.ID)
		}

		// And the event's own copy: nothing re-reads it until the stream has
		// drained, so a pointer reused across announcements turns every buffered
		// call into the last one.
		if call := was.call; call.ID != was.id || call.Name != was.name || !bytes.Equal(call.Input, was.input) {
			return fmt.Errorf("%s: the ToolCall announced as %q (%s) now reads as %q (%s); each "+
				"announcement is its own value, because the loop buffers them all before it runs any",
				at, was.id, was.input, call.ID, call.Input)
		}
	}
	return nil
}

// checkToolCall holds what EventToolCall promises: arguments complete and
// parsed, for a call that also exists in the message. The second half is design
// §4 invariant 1 one step upstream — the loop runs the tool from the event, so a
// call missing from the message leaves a tool_result answering nothing.
func checkToolCall(at string, ev Event) (int, error) {
	call := ev.ToolCall

	switch {
	case call.ID == "":
		return 0, fmt.Errorf("%s: ToolCall has no ID; the tool_result that answers it has nowhere to point", at)
	case call.Name == "":
		return 0, fmt.Errorf("%s: ToolCall has no Name; nothing can be resolved from the registry", at)
	}
	called, err := arguments(call.Input)
	if err != nil {
		return 0, fmt.Errorf("%s: ToolCall arguments %s; EventToolCall means they have arrived complete, "+
			"and a call taking none still sends {}", at, err)
	}

	for index, block := range ev.Partial.Content {
		if block.Type != BlockToolUse || block.ID != call.ID {
			continue
		}
		if block.Name != call.Name {
			return 0, fmt.Errorf("%s: ToolCall %s names %q but the tool_use block with that id names %q",
				at, call.ID, call.Name, block.Name)
		}
		recorded, err := arguments(block.Input)
		if err != nil {
			return 0, fmt.Errorf("%s: the tool_use block for %s holds arguments that %s; the message is "+
				"what gets persisted, so the fragments have to be finished there too", at, call.ID, err)
		}
		// Byte-identical rather than equal by value, to agree with the accumulation
		// rule. A rule about one stream, not about the bytes forever: json.Marshal
		// compacts a RawMessage on the first save.
		if !bytes.Equal(block.Input, call.Input) {
			if reflect.DeepEqual(recorded, called) {
				return 0, fmt.Errorf("%s: ToolCall %s and its tool_use block hold the same arguments "+
					"serialized differently (%s against %s); keep the fragments verbatim on both "+
					"sides, so the transcript is what the model actually sent",
					at, call.ID, call.Input, block.Input)
			}
			return 0, fmt.Errorf("%s: ToolCall %s runs with %s but the tool_use block records %s; "+
				"the loop dispatches from the event and the message is what gets persisted, so a "+
				"transcript that disagrees describes something that never ran",
				at, call.ID, call.Input, block.Input)
		}
		return index, nil
	}
	return 0, fmt.Errorf("%s: no tool_use block in Partial has id %q; the call has to exist in the "+
		"message as well as in the event", at, call.ID)
}

// arguments requires a JSON object rather than merely valid JSON. The difference
// is `null`, which json.Unmarshal decodes into a struct as a silent no-op — so a
// tool runs with every argument zeroed instead of failing — and which is how an
// OpenAI-compatible endpoint normalises an empty arguments string.
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

// isJSONObject answers from the first byte rather than through arguments(),
// because Block.MarshalJSON asks on every session append and a write or edit
// call carries a whole file body: decoding that tree to learn it starts with a
// brace is an allocation per line written.
func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(raw)
}

// checkAccumulation: a delta event leaves Partial holding everything so far plus
// the delta, adds it to exactly one block, and no other event may change the
// channel at all — content nobody announced is content a UI keyed on event types
// never draws. The exception is a channel's grows list, whose events may deliver
// the entire payload with nothing in Delta.
//
// It works per block rather than on the channel's blocks joined together, because
// two parallel calls can stream interleaved: a joined string would see call 2's
// first fragment land in the middle of call 1's arguments and call it a rewrite.
// Rejecting a conformant adapter is worse than missing a bug, since its author
// cannot tell which they are looking at.
func checkAccumulation(at string, ev Event, ch channel, seen map[int]string, frozen map[int]announced) error {
	now := ch.snapshot(ev.Partial)

	// Sorted, so a message naming a block index names the same one on every run.
	for _, i := range slices.Sorted(maps.Keys(seen)) {
		if _, ok := now[i]; !ok {
			return fmt.Errorf("%s: the %s block at index %d is gone; blocks are only ever added to, "+
				"and a message that loses one has lost part of the reply", at, ch.name, i)
		}
	}

	grew, added := 0, ""
	for _, i := range slices.Sorted(maps.Keys(now)) {
		text, was := now[i], seen[i]
		switch {
		case text == was:
			// Unchanged, placeholder included.
		case frozen[i].id != "":
			return fmt.Errorf("%s: Partial %s at index %d changed from %q to %q after its call was "+
				"announced; a completed call is finished, and the loop has already dispatched from it",
				at, ch.name, i, was, text)
		case ch.placeholder != "" && was == ch.placeholder && text != "":
			// A fragment replaces the placeholder rather than extending it.
			// Clearing the block is content going missing, and falls through.
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
		case grew == 0 && ev.Delta == ch.placeholder && ch.holdsPlaceholder(now):
			// The fragment was the placeholder itself, landing in a block already
			// holding one, so nothing observably changed. This is Anthropic's
			// no-argument shape: the empty object opens the block and arrives
			// again as the payload.
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

	clear(seen)
	maps.Copy(seen, now)
	return nil
}

func (ch channel) holdsPlaceholder(blocks map[int]string) bool {
	if ch.placeholder == "" {
		return false
	}
	for _, text := range blocks {
		if text == ch.placeholder {
			return true
		}
	}
	return false
}

// snapshot is this channel's content keyed by block index. The index is the
// identity: it is what the wire formats route fragments by, and it is stable
// because blocks are only ever appended.
func (ch channel) snapshot(m *Message) map[int]string {
	out := map[int]string{}
	for i, block := range m.Content {
		if block.Type == ch.kind {
			out[i] = ch.text(block)
		}
	}
	return out
}
