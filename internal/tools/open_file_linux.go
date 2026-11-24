//go:build linux

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Walk from the root descriptor, refusing symlinks at every component. Each
// component is resolved relative to a held directory FD rather than a path
// checked earlier. O_NONBLOCK avoids hanging if a file is swapped for a FIFO.
func openWorkspaceFile(root *os.Root, rel string) (*os.File, error) {
	if !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("invalid workspace path: %s", rel)
	}
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	clean := filepath.Clean(rel)
	if clean == "." {
		return dir, nil
	}
	parts := strings.Split(clean, string(filepath.Separator))
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		fd, openErr := unix.Openat(int(dir.Fd()), part, flags, 0)
		_ = dir.Close()
		if openErr != nil {
			return nil, &os.PathError{Op: "openat", Path: rel, Err: openErr}
		}
		dir = os.NewFile(uintptr(fd), rel)
		if dir == nil {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("invalid workspace descriptor")
		}
	}
	return dir, nil
}
