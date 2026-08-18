//go:build !windows

package builtin

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup makes cancelling the command reach everything it started.
// Setpgid gives the child a group of its own to lead, so its pid is also the
// group id and a signal to the negated pid reaches every member. Without it
// exec.CommandContext kills the direct child only — a documented, unresolved Go
// limitation (golang/go#21135) — and `bash -c "npm run dev &"` leaves the dev
// server holding port 3000 for as long as the machine is up.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			// The group finished between the deadline and the signal. Wait has to
			// read that as the command being done, not as a failure to cancel it.
			return os.ErrProcessDone
		}
		return err
	}
}
