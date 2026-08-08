// Package builtin holds the eight built-in tools: read, write, edit, bash,
// grep, find, ls and todos.
//
// Does not contain: path validation, which every file tool routes through
// workspace rather than touching os directly; and no approval logic, which is
// permission's. A tool asks; it does not decide.
package builtin
