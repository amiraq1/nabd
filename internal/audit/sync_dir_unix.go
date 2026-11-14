//go:build unix

package audit

import (
	"errors"
	"fmt"
	"os"
)

func syncDirectory(root *os.Root, path string) error {
	dir, err := root.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}
