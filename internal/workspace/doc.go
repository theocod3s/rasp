// Package workspace owns the os.Root handle that confines every file tool to
// the workspace, resolves paths against it, and tracks mtimes for the
// read-before-edit rule. It rejects ../ escapes and symlinks pointing outside,
// naming the offending path when it does.
//
// Does not contain: tool logic, and no permission decisions. It answers whether
// a path is inside the workspace, never whether the caller may touch what is
// there.
package workspace
