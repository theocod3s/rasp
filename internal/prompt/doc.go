// Package prompt assembles the system prompt as ordered blocks carrying cache
// flags, and discovers AGENTS.md.
//
// Does not contain: provider-specific cache syntax. It marks where a breakpoint
// belongs; the adapter decides how to say so on the wire.
package prompt
