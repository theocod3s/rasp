// Package mcp manages stdio MCP servers: spawning subprocesses, initialize,
// tools/list, namespacing, call proxying and reaping.
//
// Does not contain: permission decisions, schema interpretation, or any
// transport but stdio. And the containment rule from design §8.0 — no MCP type,
// error code or protocol concept may leave this package. A server crashing
// mid-turn leaves here as an ordinary tool error, never a panic.
package mcp
