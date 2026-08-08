// Package compact keeps a conversation inside the context window: token
// estimation, tool-output pruning and LLM summarization.
//
// Does not contain: storage. It takes a slice of entries and returns a new one,
// which is what makes every strategy in it testable without touching a disk.
package compact
