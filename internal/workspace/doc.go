// Package workspace owns the os.Root handle that confines every file tool to
// the workspace, resolves paths against it, and tracks mtimes for the
// read-before-edit rule.
//
// Does not contain: tool logic and permission decisions. It answers "is this
// path inside the workspace, and what is it" — rejecting ../ escapes and
// symlinks pointing outside, naming the offending path when it does.
package workspace
