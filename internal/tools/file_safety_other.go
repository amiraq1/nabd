//go:build !unix && !windows

package tools

import (
	"fmt"
	"os"
)

func ensureNoHardLinks(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() {
		return fmt.Errorf("cannot verify hard-link count for %s on this platform", info.Name())
	}
	return nil
}
