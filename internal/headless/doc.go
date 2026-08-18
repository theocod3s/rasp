// Package headless answers one prompt with one model call, writing the reply to
// a plain io.Writer as it streams. It renders llm.Event.Partial and remembers how
// much of each block has already gone out, so no provider wire format reaches the
// output. Anything short of a complete reply comes back as an ordinary error for
// the command to report, because a script reading the output cannot tell a half
// answer from a whole one.
//
// Does not contain: the loop (agent), any styling, any flag parsing, and no
// Bubble Tea. It is the second consumer of the event stream, which is what
// proves the stream carries no assumption that a terminal is reading it
// (design §2).
package headless
