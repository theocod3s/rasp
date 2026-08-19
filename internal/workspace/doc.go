// Package workspace owns the os.Root handle that confines every file tool to
// the workspace, resolves paths against it, tracks mtimes for the
// read-before-edit rule, and holds the per-file mutex same-file mutations
// serialize on. It rejects ../ escapes and symlinks pointing outside, naming
// the offending path when it does.
//
// Does not contain: tool logic, and no permission decisions. It answers whether
// a path is inside the workspace, never whether the caller may touch what is
// there — and it hands out the mutation lock without deciding which operations
// need one, because only the tool knows where its read-modify-write begins.
package workspace
