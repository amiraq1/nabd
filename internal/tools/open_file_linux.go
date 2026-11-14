//go:build linux

package tools

import (
	"os"
	"syscall"
)

func openWorkspaceFile(root *os.Root, rel string) (*os.File, error) {
	return root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
