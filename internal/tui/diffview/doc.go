// Package diffview renders a unified diff with colour, so every edit is visible
// without reaching for git diff in another terminal.
//
// Does not contain: diff computation — the diff arrives in a tool Result's
// Details — and no file I/O.
package diffview
