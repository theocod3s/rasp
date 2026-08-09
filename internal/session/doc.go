// Package session is the JSONL append store: reading, appending, atomic writes
// via temp and rename, listing, and repairing an interrupted turn on load.
//
// Does not contain: compaction, which is compact's. Nor message semantics
// beyond Sanitize's tool_use/tool_result pairing repair (design §6) — that is
// the one interpretation it must make, because an unrepaired turn poisons every
// request after it.
package session
