// Package permission owns the approval ladder, session grants, the three gated
// modes and their presets, glob resolution, and the yolo short-circuit ahead of
// all of it. Modes are data here, which is why the agent loop never learns their
// names.
//
// Does not contain: any rendering. It publishes a request and waits for an
// answer; someone else draws it.
package permission
