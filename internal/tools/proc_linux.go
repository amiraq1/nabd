//go:build linux || android

package tools

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// configureProcessTree configures process group isolation and process-group-aware
// cancellation for child processes on Linux and Android.
//
// Linux PR_SET_PDEATHSIG is intentionally omitted: in Linux, PDEATHSIG is bound
// to the thread lifetime (the OS thread that invoked clone/fork), not the parent
// process lifetime. In Go's M:N user-space runtime scheduler, goroutines can be
// scheduled across arbitrary OS threads, and threads may terminate during the
// application lifecycle, which could cause premature delivery of SIGKILL to child
// processes while the parent Go process remains active.
//
// Instead, reliable cleanup is achieved via:
//  1. Process group isolation (Setpgid: true).
//  2. Process-group cancellation (syscall.Kill(-pid, SIGKILL)), ensuring all descendants
//     (child and grandchildren) in the process group are terminated when cmd.Cancel is triggered.
//  3. Bounded WaitDelay (2s) preventing hanging pipe readers on stdio streams.
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
