//go:build unix

package tools

import (
	"os/exec"
	"syscall"
)

// prepareProcess puts cmd in its own process group so cancellation kills the
// whole tree, including test binaries spawned by the go command.
func prepareProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
