//go:build !unix && !windows

package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

// openSafeWebKeyFile opens the key file inside its parent directory root on
// non-Unix, non-Windows platforms.
func openSafeWebKeyFile(path string) (*os.File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("open key file directory: %w", err)
	}
	defer root.Close()

	f, err := root.Open(filepath.Base(abs))
	if err != nil {
		return nil, fmt.Errorf("open key file %s: %w", path, err)
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
	if err := ensureNoHardLinks(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
