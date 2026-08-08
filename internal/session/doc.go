// Package session is the JSONL append store: reading, appending, atomic writes
// via temp and rename, listing, and repairing an interrupted turn on load.
//
// Does not contain: compaction, which is compact's; and no message semantics —
// it persists entries without interpreting them.
package session
