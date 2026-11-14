//go:build linux

package tools

import (
	"fmt"
	"os"
	"syscall"
)

// ensureNoHardLinks rejects multiply-linked regular files. os.Root prevents
// pathname escapes, but hard links refer to the same inode without a path to
// resolve; checking the opened descriptor closes that gap for file reads.
func ensureNoHardLinks(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot verify hard-link count for %s", info.Name())
	}
	if st.Nlink > 1 {
		return fmt.Errorf("refusing multiply-linked file %s (link count %d)", info.Name(), st.Nlink)
	}
	return nil
}
