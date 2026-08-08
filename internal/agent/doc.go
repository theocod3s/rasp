// Package agent runs the loop: it drives each step, owns step state, enforces
// the loop invariants and emits the events every consumer renders from.
//
// Does not contain: any terminal code, any HTTP, any filesystem syscall, and no
// knowledge of modes whatsoever — the loop never branches on plan, manual, auto
// or yolo. Those are permission presets, and permission resolves them.
package agent
