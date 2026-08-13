package llm

import (
	"context"
	"iter"
)

// StreamResponse is a pull-based iterator over stream events.
//
// CONTRACT — both halves matter:
//
//  1. It MUST NOT return a Go error for model, request or runtime failures.
//     Those arrive as a terminal Event{Type: EventError}. One error path, not
//     two, which is why the retry classifier can be a pure function over a
//     Message instead of a tangle of error-type switches (design §12).
//
//  2. Every event carries Partial — the FULL accumulated message so far, not
//     just the delta. Consumers render Partial. They never reassemble deltas
//     themselves, which deletes an entire class of interleaving bug and keeps
//     the differences between provider wire formats from reaching the UI.
//
// Partial is a pointer to one message the provider allocates outside its
// stream loop and mutates in place, so honouring (2) costs eight bytes an
// event rather than a copy per token. Allocating inside the loop satisfies the
// letter of the contract and throws away the reason it is affordable.
//
// CheckStream is these rules in executable form; every implementation is
// expected to pass it.
type StreamResponse = iter.Seq[Event]

// Provider is one model API. Adapters are the only packages that know a wire
// format; everything above them sees Message, Block and Event.
type Provider interface {
	// ID is the stable identifier: "anthropic", "openrouter", "ollama".
	ID() string

	// Stream runs exactly one model call. See the StreamResponse contract —
	// in particular that failures come back through the returned sequence,
	// which is why there is no error result to ignore.
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

	// Tools comes from one registry snapshot per turn, sorted by name. The
	// tool list sits inside the cached prompt prefix, so an unstable order
	// silently destroys the cache on every request (design §3.3).
	Tools []ToolSpec

	MaxTokens int
	Thinking  ThinkingConfig
}

// SystemBlock is one piece of the system prompt, with the cache flag that
// decides what a change to it costs.
type SystemBlock struct {
	Text  string
	Cache bool // place a cache breakpoint AFTER this block
}

// ToolSpec is a tool as the model is told about it. It is the part of tool.Tool
// a request needs — a name, a description and a schema, but no way to run
// anything — and it lives here because the dependency runs the other way: tool
// imports llm to produce these (design §3.3).
//
// Schema is a decoded JSON Schema object, passed through as it arrived. It is
// the only representation a reflected Go struct and a server-supplied MCP
// schema can both produce, and adapters may strip keywords a given API rejects
// without re-deriving anything.
type ToolSpec struct {
	Name        string
	Description string
	Schema      map[string]any
}

// ThinkingConfig asks for extended thinking. The zero value asks for none, so a
// caller that has no opinion writes nothing.
type ThinkingConfig struct {
	Enabled bool

	// BudgetTokens caps the thinking tokens. Zero leaves the choice to the
	// adapter, which knows what the model requires — Anthropic rejects a
	// budget below its own floor, and a caller here should not have to know
	// what that floor is this month.
	BudgetTokens int
}
