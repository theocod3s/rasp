// Package diffview renders a unified diff with colour and a line-number gutter,
// so every edit is visible without reaching for git diff in another terminal.
//
// Does not contain: diff computation — the diff arrives in a tool Result's
// Details — and no file I/O, which is why the numbers are read off the hunk
// headers rather than counted in the file they belong to.
package diffview
