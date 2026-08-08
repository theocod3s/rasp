// Package anthropic adapts the native Anthropic API onto llm.Provider: wire
// translation in both directions, cache breakpoints, thinking blocks and
// tool_use.
//
// Does not contain: retry policy, which is llm/retry's job and is shared with
// every other adapter; and no tool semantics — it transports tool calls without
// interpreting them.
package anthropic
