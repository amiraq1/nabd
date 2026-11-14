//go:build !unix

package tools

import "os/exec"

func prepareProcess(cmd *exec.Cmd) {}
