//go:build windows

package main

import (
	"os"
	"os/exec"
	"strconv"
)

// configureClipboardProcess terminates the clipboard process tree on context
// expiration using taskkill /T as best effort with Process.Kill fallback.
func configureClipboardProcess(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		if err := kill.Run(); err == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}

// killClipboardProcessTree performs best-effort termination of the Windows
// process tree if WaitDelay expired with an orphan child holding I/O open.
func killClipboardProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	if err := kill.Run(); err != nil {
		_ = cmd.Process.Kill()
	}
}
