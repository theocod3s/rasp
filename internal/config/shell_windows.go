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
// The line starts with the program name because CmdLine is the whole command
// line CreateProcess hands the child, and a child skips its first token as its
// own name — exactly as it would when the line is built from Args. Writing
// just "/c …" would spend the switch on that slot and leave cmd.exe reading
// the command as though no /c had been given.
//
// Untested: design §14 cross-compiles the Windows targets from a linux runner
// and runs no tests on them, so this path is checked by the compiler and by
// reading, not by execution. M5-10 verifies it on a real Windows runner before
// v1.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "cmd /c " + command}
	return cmd
}
