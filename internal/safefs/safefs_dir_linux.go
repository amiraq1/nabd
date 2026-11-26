//go:build linux

package safefs

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openRootLookupDir() (*os.File, error) {
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
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

// openLookupDirAtNoFollow opens an ancestor directory component with O_PATH and
// O_NOFOLLOW on Linux/Android so traverse-only (--x, e.g. /data/data on
// Android) directories can be stepped through securely without read access.
func openLookupDirAtNoFollow(parentFD int, name string) (*os.File, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%w: %s", ErrSymlink, name)
		}
		if errors.Is(err, unix.ENOTDIR) {
			var st unix.Stat_t
			if fErr := unix.Fstatat(parentFD, name, &st, unix.AT_SYMLINK_NOFOLLOW); fErr == nil && (uint32(st.Mode)&unix.S_IFMT) == unix.S_IFLNK {
				return nil, fmt.Errorf("%w: %s", ErrSymlink, name)
			}
			return nil, fmt.Errorf("%w: %s", ErrNotDirectory, name)
		}
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	if f == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("invalid lookup directory descriptor for %s", name)
	}
	return f, nil
}
