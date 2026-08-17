package llm

import (
	"context"
	"iter"
)

// StreamResponse is a pull-based iterator over stream events. CheckStream is
// the contract below in executable form; every implementation must pass it.
//
//  1. It MUST NOT return a Go error for model, request or runtime failures.
//     Those arrive as a terminal Event{Type: EventError}. One error path, not
//     two, is what lets the retry classifier be a pure function over a Message
//     (design §12).
//
//  2. Every event carries Partial — the FULL accumulated message so far, not
//     just the delta. Consumers render Partial and never reassemble deltas,
//     which keeps the differences between provider wire formats out of the UI.
//
// Partial is one message the provider allocates outside its stream loop and
// mutates in place, so (2) costs eight bytes an event rather than a copy per
// token. Allocating inside the loop meets the letter of the contract and throws
// away the reason it is affordable.
//
// EventMessageStart is OPTIONAL — an OpenAI-compatible stream has no equivalent —
// so nothing may key its initialisation on that event. When it does arrive it
// comes first.
type StreamResponse = iter.Seq[Event]

// Provider is one model API. Adapters are the only packages that know a wire
// format; everything above them sees Message, Block and Event.
type Provider interface {
	// ID is the stable identifier: "anthropic", "openrouter", "ollama".
	ID() string

	// Stream runs exactly one model call. Failures come back through the
	// returned sequence — see the StreamResponse contract.
	Stream(ctx context.Context, req Request) StreamResponse

	// Efforts is the subset of the ladder this provider can put on the wire, in
	// ladder order. Per protocol, never per model: no adapter recognises a model
	// id (scope.md), so a rung can be offered here and still rejected by the API.
	//
	// Required rather than an optional interface someone type-asserts, because an
	// adapter that had not implemented it would read as one allowing every rung.
	Efforts() []Effort
}

// Request is one model call.
type Request struct {
	Model string

	// System is ordered, and the order is load-bearing: stable text first so a
	// cache breakpoint can sit behind it (design §11). The adapter applies the
	// provider's cache syntax; nothing above it does.
	System []SystemBlock

	// Messages is the transcript, and not all of it is sendable: a turn can break
	// off leaving a message with nothing left for an adapter to put on the wire.
	// See CheckSendable, which takes the blocks an adapter kept rather than the
	// message it started from.
	Messages []Message

	// Tools comes from one registry snapshot per turn, sorted by name. The list
	// sits inside the cached prompt prefix, so an unstable order silently
	// destroys the cache on every request (design §3.3).
	Tools []ToolSpec

	MaxTokens int
	Thinking  ThinkingConfig

	// Effort is a sibling of Thinking rather than a field inside it: the two wire
	// fields are independent, and "effort high, thinking off" is a request a
	// provider will honour.
	Effort Effort
}

// SystemBlock is one piece of the system prompt.
type SystemBlock struct {
	Text  string
	Cache bool // place a cache breakpoint AFTER this block
}

// ToolSpec is a tool as the model is told about it. It lives here rather than in
// tool because the dependency runs the other way — tool imports llm to produce
// these (design §3.3).
//
// Schema is a decoded JSON Schema object, passed through as it arrived: the only
// representation a reflected Go struct and a server-supplied MCP schema can both
// produce, and one an adapter can strip keywords from without re-deriving.
type ToolSpec struct {
	Name        string
	Description string
	Schema      map[string]any
}

// ThinkingConfig asks for extended thinking. The zero value asks for none. Depth
// is not a thinking field on either provider's wire — it is Request.Effort.
type ThinkingConfig struct {
	Enabled bool
}
