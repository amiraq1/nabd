//go:build windows

package audit

import (
	"os"

	"golang.org/x/sys/windows"
)

type fileID struct {
	volume uint64
	index  uint64
}

func fileIdentity(f *os.File) (fileID, error) {
	var bhfi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &bhfi); err != nil {
		return fileID{}, err
	}
	index := uint64(bhfi.FileIndexHigh)<<32 | uint64(bhfi.FileIndexLow)
	return fileID{volume: uint64(bhfi.VolumeSerialNumber), index: index}, nil
}
