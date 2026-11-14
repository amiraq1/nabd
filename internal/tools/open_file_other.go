//go:build !unix && !windows

package tools

import "os"

func openWorkspaceFile(root *os.Root, rel string) (*os.File, error) {
	return root.OpenFile(rel, os.O_RDONLY, 0)
}
