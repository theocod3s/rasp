package agent

import (
	"encoding/json"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tool"
)

// EventKind names what an Event carries.
//
// Design §3.5 lists more kinds than this. The absent ones are produced by
// packages the loop does not compose yet — a permission prompt, a compaction, a
// mode switch — and each arrives with its producer rather than as a kind nothing
// can emit.
type EventKind string

const (
	EventAssistantDelta EventKind = "assistant_delta"
	EventAssistantEnd   EventKind = "assistant_end"
	EventToolStart      EventKind = "tool_start"
	EventToolEnd        EventKind = "tool_end"
	EventTurnEnd        EventKind = "turn_end"
	EventError          EventKind = "error"
)

// Event is what the loop tells a frontend, and the whole of what it tells one:
// no consumer reaches into agent state, which is the seam that makes a second
// frontend cost nothing (design §1).
//
// A turn ends with exactly one EventTurnEnd, whether it completed, failed or was
// interrupted, and a failed one emits EventError first. Anything tracking
// "busy" can key on that alone.
type Event struct {
	Kind EventKind

	// Message on EventAssistantDelta is the provider's live accumulation — the
	// same pointer for the whole step, so it is rendered where it stands and
	// never stored. On EventAssistantEnd it is the copy that went into the
	// transcript, which does not change again.
	Message *llm.Message

	CallID string
	Tool   string
	Input  json.RawMessage
	Result *tool.Result

	// Usage on EventTurnEnd is the turn's total across every step it took, which
	// is what a cost readout wants. Context estimation wants the last step's
	// alone and reads that off the transcript instead (design §11).
	Usage llm.Usage

	Err error
}
