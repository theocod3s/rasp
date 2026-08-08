// Package openaicompat adapts any OpenAI-compatible endpoint onto llm.Provider.
// The base URL is configuration, which is what makes one adapter serve
// OpenRouter, Groq, DeepSeek, Ollama, LM Studio and the rest.
//
// Does not contain: retry policy (llm/retry), tool semantics, or any assumption
// that the endpoint is OpenAI's own. A quirk that cannot be expressed as
// configuration does not belong here either.
package openaicompat
