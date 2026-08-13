package llm

import (
	"errors"
	"fmt"
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
func CheckStream(seq StreamResponse) ([]Event, error) {
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
	return events, nil
}

// streamChecker carries what checking one event needs to know about the events
// before it: which message they pointed at, how much had accumulated, and
// whether the stream was already over.
type streamChecker struct {
	partial  *Message  // the pointer the first event carried
	text     string    // text found in Partial on the previous event
	thinking string    // and thinking, tracked separately
	terminal EventType // the terminal event's type, once one has arrived
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

	if err := checkAccumulation(at, ev, BlockText, &c.text); err != nil {
		return err
	}
	if err := checkAccumulation(at, ev, BlockThinking, &c.thinking); err != nil {
		return err
	}

	if ev.Type == EventDone || ev.Type == EventError {
		c.terminal = ev.Type
		switch {
		case ev.StopReason == "":
			return fmt.Errorf("%s has no StopReason; the terminal event says why the model stopped", at)
		case ev.Partial.StopReason != ev.StopReason:
			return fmt.Errorf("%s has StopReason %q but Partial.StopReason is %q; "+
				"the message is what gets persisted and what the retry classifier reads, "+
				"so it carries the reason too", at, ev.StopReason, ev.Partial.StopReason)
		}
	}
	return nil
}

// checkAccumulation holds the half of the contract that is easiest to satisfy
// on paper and get wrong in code: a delta event must leave Partial holding
// everything so far plus the delta, and no event may lose ground. seen is what
// the previous event held, and is advanced.
func checkAccumulation(at string, ev Event, kind BlockType, seen *string) error {
	got := blockText(ev.Partial, kind)

	isDelta := (kind == BlockText && ev.Type == EventTextDelta) ||
		(kind == BlockThinking && ev.Type == EventThinkingDelta)

	if isDelta {
		if want := *seen + ev.Delta; got != want {
			return fmt.Errorf("%s: Partial %s is %q, want %q — Partial is the full accumulated message, not the delta",
				at, kind, got, want)
		}
	} else if !strings.HasPrefix(got, *seen) {
		return fmt.Errorf("%s: Partial %s is %q, which does not start with the %q already streamed; "+
			"accumulated content is never rewritten or dropped", at, kind, got, *seen)
	}

	*seen = got
	return nil
}

// blockText joins the text of every block of one kind, which is how much of
// that kind the message holds regardless of how the provider split it.
func blockText(m *Message, kind BlockType) string {
	var b strings.Builder
	for _, block := range m.Content {
		if block.Type == kind {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}
