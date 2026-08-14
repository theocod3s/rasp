package llm

import "encoding/json"

type EventType string

const (
	EventMessageStart   EventType = "message_start"
	EventTextDelta      EventType = "text_delta"
	EventThinkingDelta  EventType = "thinking_delta"
	EventToolInputStart EventType = "tool_input_start"
	EventToolInputDelta EventType = "tool_input_delta"

	// EventToolCall means one tool call's arguments are complete and parsed.
	// Anything wanting to run a tool waits for this rather than reassembling
	// EventToolInputDelta fragments.
	EventToolCall EventType = "tool_call"

	EventDone  EventType = "done"
	EventError EventType = "error"
)

// Event is one step of a streamed response. Exactly one Event is terminal —
// EventDone or EventError — and it is the last one.
type Event struct {
	Type EventType

	// Delta is only the newly-arrived text, on the event types that carry text.
	// A convenience for anything that appends rather than re-renders, and never
	// authoritative: Partial is.
	Delta string

	// Partial is the FULL accumulated message so far, on every event, and it is
	// the same pointer every time — so storing it stores a view that keeps
	// changing, not a snapshot. See the StreamResponse contract.
	Partial *Message

	// ToolCall is set on EventToolCall and on nothing else, and it is a fresh
	// value each time — unlike Partial, deliberately the same pointer throughout.
	// The loop buffers every announcement and dispatches only once the stream has
	// drained, so one reused *ToolCall would leave it holding N pointers to the
	// last call.
	ToolCall *ToolCall

	// StopReason is set on the terminal event and matches Partial.StopReason. The
	// message carries it too because the message is what gets persisted and what
	// the retry classifier reads (design §12).
	StopReason StopReason

	// Err is set on EventError and on nothing else, and it is informational: the
	// control path is the event type and the stop reason.
	Err error
}

// ToolCall is a tool invocation whose arguments have fully arrived and parsed.
// It mirrors the tool_use block the same call becomes in the transcript, so
// nothing has to reach into Partial to find what it was asked to run.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}
