//go:build windows

package config

import (
	"context"
	"os/exec"
	"syscall"
)

// shellCommand runs one `$(…)` through cmd.exe.
//
// The command line is set directly rather than passed as an argument, because
// the two sides disagree about quoting. Go escapes arguments by
// CommandLineToArgvW's rules, where an inner `"` becomes `\"`; cmd.exe does not
// follow those rules, and would run `printf \"%s\" x` for a value that read
// `printf "%s" x`. Handing cmd the line verbatim gives the user what they
// typed, which is the same thing the unix side gets from `sh -c`.
//
// Untested: design §14 cross-compiles the Windows targets from a linux runner
// and runs no tests on them, so this path is checked by the compiler and by
// reading, not by execution.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "/c " + command}
	return cmd
}
