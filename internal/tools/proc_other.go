//go:build !unix

package tools

import "os/exec"

//lint:ignore U1000 paired with the platform implementations
func prepareProcess(cmd *exec.Cmd) {}
