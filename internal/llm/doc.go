// Package llm defines the provider-neutral message model — Message, Block,
// Event — and the Provider interface every adapter satisfies.
//
// Does not contain: provider-specific structs, wire formats or SDK types. Those
// live in the adapter subpackages, and nothing an adapter knows may leak out
// through these types.
package llm
