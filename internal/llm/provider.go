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
// so nothing may key its initialisation on that event.
type StreamResponse = iter.Seq[Event]

// Provider is one model API. Adapters are the only packages that know a wire
// format; everything above them sees Message, Block and Event.
type Provider interface {
	// ID is the stable identifier: "anthropic", "openrouter", "ollama".
	ID() string

	// Stream runs exactly one model call. Failures come back through the
	// returned sequence — see the StreamResponse contract.
	Stream(ctx context.Context, req Request) StreamResponse
}

// Request is one model call.
type Request struct {
	Model string

	// System is ordered, and the order is load-bearing: stable text first so a
	// cache breakpoint can sit behind it (design §11). The adapter applies the
	// provider's cache syntax; nothing above it does.
	System []SystemBlock

	Messages []Message

	// Tools comes from one registry snapshot per turn, sorted by name. The list
	// sits inside the cached prompt prefix, so an unstable order silently
	// destroys the cache on every request (design §3.3).
	Tools []ToolSpec

	MaxTokens int
	Thinking  ThinkingConfig
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

// ThinkingConfig asks for extended thinking. The zero value asks for none.
type ThinkingConfig struct {
	Enabled bool

	// BudgetTokens caps the thinking tokens. Zero leaves the choice to the
	// adapter: Anthropic rejects a budget below its own floor, and a caller
	// here should not have to know what that floor is this month.
	BudgetTokens int
}
