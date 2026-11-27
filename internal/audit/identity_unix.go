//go:build unix

package audit

import (
	"os"

	"golang.org/x/sys/unix"
)

type fileID struct {
	dev uint64
	ino uint64
}

func fileIdentity(f *os.File) (fileID, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return fileID{}, err
	}
	return fileID{dev: uint64(st.Dev), ino: uint64(st.Ino)}, nil
}
