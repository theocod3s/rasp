// Package headless answers one prompt with one model call, writing the reply to
// a plain io.Writer as it streams and returning a failure as an ordinary error
// for the command to report. It renders llm.Event.Partial and remembers how much
// of it has already gone out, so no provider wire format reaches the output.
//
// Does not contain: the loop (agent), any styling, any flag parsing, and no
// Bubble Tea. It is the second consumer of the event stream, which is what
// proves the stream carries no assumption that a terminal is reading it
// (design §2).
package headless
