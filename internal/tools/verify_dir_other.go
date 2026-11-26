//go:build !unix

package tools

import (
	"fmt"
	"os"
)

func verifyOpenedDirNoFollow(parentRoot *os.Root, part string, _ *os.Root) error {
	info, err := parentRoot.Lstat(part)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink path components are not allowed: %s", part)
	}
	if !info.IsDir() {
		return fmt.Errorf("parent path is not a directory: %s", part)
	}
	return nil
}
