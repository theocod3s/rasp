package llm

import "encoding/json"

// EventType is which kind of stream event arrived.
type EventType string

const (
	EventMessageStart   EventType = "message_start"
	EventTextDelta      EventType = "text_delta"
	EventThinkingDelta  EventType = "thinking_delta"
	EventToolInputStart EventType = "tool_input_start"
	EventToolInputDelta EventType = "tool_input_delta"

	// EventToolCall means one tool call's arguments are complete and parsed.
	// Fragments arrive as EventToolInputDelta and are nobody's problem above
	// the provider; a consumer that wants to run a tool waits for this.
	EventToolCall EventType = "tool_call"

	EventDone  EventType = "done"
	EventError EventType = "error"
)

// Event is one step of a streamed response. Exactly one Event is terminal —
// EventDone or EventError — and it is the last one.
type Event struct {
	Type EventType

	// Delta is only the newly-arrived text, for the event types that carry
	// text. It is a convenience for anything that appends rather than
	// re-renders, and it is never authoritative: Partial is.
	Delta string

	// Partial is the FULL accumulated message so far, and it is populated on
	// every event. It is also the same pointer every time — the provider
	// mutates one message in place — so a consumer that stores it stores a
	// view that keeps changing, not a snapshot. See the StreamResponse
	// contract.
	Partial *Message

	// ToolCall is set on EventToolCall and on nothing else.
	ToolCall *ToolCall

	// StopReason is set on the terminal event, and matches Partial.StopReason.
	// The message carries it because the message is what gets persisted and
	// what the retry classifier reads (design §12).
	StopReason StopReason

	// Err is set on EventError and on nothing else. It is informational — the
	// control path is the event type and the stop reason, not a Go error
	// travelling beside them.
	Err error
}

// ToolCall is a tool invocation whose arguments have fully arrived and parsed.
// It mirrors the tool_use block the same call becomes in the transcript, so a
// consumer never has to reach into Partial to find what it was asked to run.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}
