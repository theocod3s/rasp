// Package edit implements the four-rung match ladder and the re-indentation
// that makes rung four safe. It is its own package because it is the hard part.
//
// Does not contain: file I/O. Every export is a pure string function, which is
// precisely what makes the ladder fuzzable (design §13) — the worst outcome it
// hunts for is a match that succeeds in the wrong place.
package edit
