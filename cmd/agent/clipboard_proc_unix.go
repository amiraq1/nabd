//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// configureClipboardProcess puts the clipboard command in its own process group
// and cancels the entire group on context expiration so forked descendants
// holding pipes cannot outlive the timeout.
func configureClipboardProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
}

// killClipboardProcessTree performs best-effort termination of the process
// group if a child exited early while a descendant still held I/O pipes open.
func killClipboardProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
