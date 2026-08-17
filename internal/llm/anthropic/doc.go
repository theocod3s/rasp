// Package anthropic adapts the native Anthropic API onto llm.Provider: wire
// translation, and cache breakpoints on the way out.
//
// Does not contain: retry policy, which is llm/retry's job and is shared with
// every other adapter; no tool support in either direction yet, so a request
// carrying tools is refused rather than sent without them and tool_use has no
// mapping on the way back; and no way to send a thinking block, which arrives
// and is rendered but is dropped on replay because llm.Block has nowhere to keep
// the signature Anthropic requires with it. All three arrive later; until then
// the refusals are what keeps a half-translated request off the wire.
package anthropic
