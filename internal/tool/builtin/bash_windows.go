//go:build windows

package builtin

import "os/exec"

// setProcessGroup is deliberately empty on Windows, which leaves cancellation as
// exec.CommandContext defines it: the command itself is killed and a process it
// started outlives it. Windows has no process group to signal — the equivalent
// is a Job Object, which needs golang.org/x/sys/windows and its own lifetime
// handling, and that is a dependency this has not earned yet. WaitDelay still
// bounds the wait, so a survivor holding the output pipe cannot hang the turn.
//
// Untested rather than merely weaker: design §14 cross-compiles the Windows
// targets from a linux runner and runs no tests on them.
func setProcessGroup(*exec.Cmd) {}
