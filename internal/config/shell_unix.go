//go:build !windows

package config

import (
	"context"
	"os/exec"
)

// shellCommand runs one `$(…)` the way the syntax promises: through a shell,
// so a pipe in `$(cat secret | head -1)` is a pipe.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", command)
}
