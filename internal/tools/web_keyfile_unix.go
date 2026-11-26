//go:build unix

package tools

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// openSafeWebKeyFile opens path without following a symlink at the final path
// component and verifies file type, size, permissions, and link count on the
// opened descriptor rather than using a separate Lstat call.
func openSafeWebKeyFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open key file %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("invalid key file descriptor for %s", path)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat key file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("insecure key file: not a regular file: %s", path)
	}
	if info.Size() > maxWebKeyFileBytes {
		_ = f.Close()
		return nil, fmt.Errorf("insecure key file: size %d bytes exceeds %d-byte limit: %s", info.Size(), maxWebKeyFileBytes, path)
	}
	perm := info.Mode().Perm()
	if perm&0o077 != 0 || perm&0o111 != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("insecure key file permissions %04o for %s: must be 0600 or stricter", perm, path)
	}
	if err := ensureNoHardLinks(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
