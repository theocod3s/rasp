// Package tui is the Bubble Tea v2 root model: it owns all rendering and all
// input handling, and the one goroutine that carries agent events into Update.
//
// The bridge lives on this side of the seam because the agent knows only
// agent.Config.Events — a callback, on the turn's own goroutine, with no idea
// what consumes it (design §6).
//
// Does not contain: any business logic. When the UI needs a decision it asks
// agent or permission and renders the answer. A rule that lives only in the
// view layer is a rule that headless mode silently loses.
package tui
