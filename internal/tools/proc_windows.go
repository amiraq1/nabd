//go:build windows

package tools

import (
	"fmt"
	"os/exec"
)

type processTree struct{ cmd *exec.Cmd }

func configureProcessTree(cmd *exec.Cmd) *processTree { return &processTree{cmd: cmd} }

func (t *processTree) Prepare() error {
	return fmt.Errorf("external process execution is disabled: full descendant containment is unavailable on Windows")
}

func (t *processTree) Cleanup() error { return nil }
