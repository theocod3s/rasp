package tool

import (
	"context"
	"encoding/json"
)

// Tool is one callable the model can invoke. A built-in derives its schema by
// reflection over a tagged Go struct; an MCP tool is handed one by a server at
// runtime. One interface with two producers is what lets an MCP tool sit in the
// same registry, pass the same permission gate, and reach the model looking like
// any other tool (design §3.2).
type Tool interface {
	Name() string
	Description() string

	// Schema is a JSON Schema object describing the tool's input. map[string]any
	// is the only representation both producers can emit, and it lets a provider
	// adapter drop keywords its API rejects without re-deriving anything.
	//
	// An MCP tool's schema is opaque and passes through untouched. MCP revision
	// 2026-07-28 accepts the whole JSON Schema 2020-12 keyword set on
	// inputSchema, $ref composition included, so anything assuming a flat object
	// with a properties map breaks on a conforming server. We do not normalize,
	// validate or re-derive it.
	Schema() map[string]any

	// Run executes one call. raw is the model's arguments as they arrived,
	// neither unmarshalled nor validated. A returned error means the tool could
	// not run at all; a tool that ran and failed says so in its Result instead.
	Run(ctx context.Context, raw json.RawMessage) (Result, error)
}

// Sequential is optional, and a tool that does not implement it runs
// concurrently with its siblings: parallel is the default, and one sequential
// call makes the whole batch serial. A tool declaring itself sequential usually
// means "I touch global state", so running it beside unknown siblings defeats
// the point (design §6 rule 4).
//
// The runtime decides and the model is never asked, which removes a trust
// question rather than answering it. MCP tools implement this and return true,
// inverting the default for code we did not audit (design §8.2).
type Sequential interface {
	Sequential() bool
}
