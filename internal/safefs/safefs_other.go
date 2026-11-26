//go:build !unix && !windows

package safefs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OpenFileNoFollow performs component-wise Lstat symlink rejection on non-Unix,
// non-Windows platforms before opening and statting the descriptor.
func OpenFileNoFollow(path string, pol OpenPolicy) (*os.File, os.FileInfo, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsAny(path, "\x00\r\n") {
		return nil, nil, fmt.Errorf("%w: %q", ErrInvalidPath, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	parts := strings.Split(strings.TrimLeft(abs, string(filepath.Separator)), string(filepath.Separator))
	cur := string(filepath.Separator)
	for i, part := range parts {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		linfo, lerr := os.Lstat(cur)
		if lerr != nil {
			return nil, nil, lerr
		}
		if linfo.Mode()&os.ModeSymlink != 0 {
			return nil, nil, fmt.Errorf("%w: %s", ErrSymlink, cur)
		}
		if i < len(parts)-1 && !linfo.IsDir() {
			return nil, nil, fmt.Errorf("%w: %s", ErrNotDirectory, cur)
		}
	}
	linfo, err := os.Lstat(abs)
	if err != nil {
		return nil, nil, err
	}
	if linfo.Mode()&os.ModeSymlink != 0 {
		return nil, nil, ErrSymlink
	}
	if !linfo.Mode().IsRegular() {
		return nil, nil, ErrNotRegularFile
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !os.SameFile(linfo, info) || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, ErrNotRegularFile
	}
	if pol.MaxBytes > 0 && info.Size() > pol.MaxBytes {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%w (size %d > %d bytes)", ErrTooLarge, info.Size(), pol.MaxBytes)
	}
	return f, info, nil
}
