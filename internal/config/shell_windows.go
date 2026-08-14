//go:build windows

package config

import (
	"context"
	"os/exec"
	"syscall"
)

// shellCommand runs one `$(…)` through cmd.exe.
//
// CmdLine is set directly rather than passed as arguments, because the two sides
// disagree about quoting: Go escapes by CommandLineToArgvW's rules, where an
// inner `"` becomes `\"`, and cmd.exe does not follow them — it would run
// `printf \"%s\" x` for a value that read `printf "%s" x`.
//
// The line starts with the program name because CmdLine is the whole command
// line CreateProcess hands the child, and a child skips its first token as its
// own name. Writing just "/c …" would spend the switch on that slot.
//
// Untested: design §14 cross-compiles the Windows targets from a linux runner
// and runs no tests on them, so this path is checked by the compiler and by
// reading. A real Windows runner verifies it before v1.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "cmd /c " + command}
	return cmd
}
