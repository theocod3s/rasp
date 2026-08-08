// Package tui is the Bubble Tea v2 root model: it owns all rendering and all
// input handling.
//
// Does not contain: any business logic. When the UI needs a decision it asks
// agent or permission and renders the answer. A rule that lives only in the
// view layer is a rule that headless mode silently loses.
package tui
