//go:build !unix && !windows

package tools

import "os/exec"

func configureProcessTree(cmd *exec.Cmd) {}
