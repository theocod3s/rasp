// Package openaicompat adapts any OpenAI-compatible endpoint onto llm.Provider
// by wrapping the real OpenAI client with injectable hooks. The base URL is
// configuration, which is what makes one adapter serve OpenRouter, Groq,
// DeepSeek, Ollama, LM Studio and the rest.
//
// Does not contain: retry policy (llm/retry), tool semantics, or a
// reimplemented OpenAI client — a provider's quirk goes in a hook, never in a
// fork of the upstream one.
package openaicompat
