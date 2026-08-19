// Package openaicompat adapts any OpenAI-compatible endpoint onto llm.Provider by
// wrapping the real OpenAI client. The base URL and the provider id are
// configuration, which is what makes one adapter serve OpenRouter, Groq, DeepSeek,
// Ollama, LM Studio and the rest.
//
// The work is the projection. This wire has no block model — one growing content
// string and a tool_calls array with its own index, no per-block stop event, and a
// function name that arrives only on a call's first fragment — so the mapping onto
// the neutral message's block indexes lives here, in shape, and is the one thing
// llm.CheckStream cannot check for us (design §3.1a).
//
// Does not contain: retry policy (llm/retry), tool semantics, or a reimplemented
// OpenAI client — an endpoint's quirk goes in a hook over the upstream client,
// never in a fork of it. Nothing needs a hook yet: every dialect met so far
// differs only in what it leaves out.
package openaicompat
