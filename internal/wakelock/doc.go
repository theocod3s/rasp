// Package wakelock holds a per-platform idle-sleep inhibitor for the duration
// of a turn, so a long turn does not die when the machine suspends.
//
// Does not contain: any decision about when a turn runs — it is told to hold
// and told to release — and no UI or logging beyond debug.
package wakelock
