//go:build windows

package tools

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// configureProcessTree configures process tree cleanup on Windows.
// Because Windows lacks native Unix process groups (Setpgid), cmd.Cancel uses
// taskkill /T /F /PID to terminate the process tree, falling back to direct
// Process.Kill().
//
// Limitations: On Windows, without suspended process creation (CREATE_SUSPENDED),
// there is an unavoidable window before process assignment. Therefore, Windows
// process tree management is classified as best-effort PARTIAL.
func configureProcessTree(cmd *exec.Cmd) {
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 2 * time.Second
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil || cmd.Process.Pid <= 0 {
			return os.ErrProcessDone
		}
		// taskkill /T kills the specified process and any child processes started by it.
		// /F forces the processes to terminate.
		kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		if err := kill.Run(); err == nil {
			return nil
		}
		// Fallback to direct process kill if taskkill fails or process already exited.
		err := cmd.Process.Kill()
		if err == nil || errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
}
