// Package chat renders the message list, maintaining a per-item render cache so
// a streaming token re-renders one item rather than the transcript, and
// rendering markdown that is still arriving.
//
// Does not contain: business logic, provider knowledge, or the diff renderer —
// that is tui/diffview. It renders what the event already carries.
package chat
