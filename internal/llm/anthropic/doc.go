// Package anthropic adapts the native Anthropic API onto llm.Provider: wire
// translation in both directions, cache breakpoints and thinking blocks.
//
// Does not contain: retry policy, which is llm/retry's job and is shared with
// every other adapter; and no tool support in either direction yet — a request
// carrying tools is refused rather than sent without them, and tool_use has no
// mapping on the way back. Both arrive with tool dispatch; until then the
// refusals are what keeps a half-translated request off the wire.
package anthropic
