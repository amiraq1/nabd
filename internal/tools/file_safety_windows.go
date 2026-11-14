//go:build windows

package tools

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func ensureNoHardLinks(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	var data windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &data); err != nil {
		return fmt.Errorf("verify hard-link count for %s: %w", info.Name(), err)
	}
	if data.NumberOfLinks > 1 {
		return fmt.Errorf("refusing multiply-linked file %s (link count %d)", info.Name(), data.NumberOfLinks)
	}
	return nil
}
