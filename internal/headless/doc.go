// Package headless consumes agent events for `rasp run -p`, printing tokens as
// they arrive and exiting with a status that reflects the outcome. It also
// drops ANSI when stdout is not a TTY, so piped output stays clean.
//
// Does not contain: the loop (agent), and no Bubble Tea. It is the second
// consumer that proves the event stream is genuinely UI-agnostic.
package headless
