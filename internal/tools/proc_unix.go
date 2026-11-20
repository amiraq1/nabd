//go:build (darwin || dragonfly || freebsd || netbsd || openbsd) && !linux && !android

package tools

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// configureProcessTree puts the child in its own process group and kills the
// whole group when the context is canceled, ensuring grandchildren cannot leak.
func configureProcessTree(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	} else {
		cmd.SysProcAttr.Setpgid = true
	}
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 2 * time.Second
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil || cmd.Process.Pid <= 0 {
			return os.ErrProcessDone
		}
		// Kill the entire process group (-pid) with SIGKILL.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == nil || err == syscall.ESRCH || errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		// Fallback to direct process termination if process-group kill encounters an error.
		fallbackErr := cmd.Process.Kill()
		if fallbackErr == nil || errors.Is(fallbackErr, os.ErrProcessDone) {
			return nil
		}
		return err
	}
}
