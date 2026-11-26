//go:build unix && !linux

package safefs

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openRootLookupDir() (*os.File, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "/")
	if f == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("invalid root descriptor")
	}
	return f, nil
}

func openLookupDirAtNoFollow(parentFD int, name string) (*os.File, error) {
	return OpenDirAtNoFollow(parentFD, name)
}
