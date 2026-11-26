package tools

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxSandboxWorkspaceMasks = 512

// collectSensitiveWorkspaceMasks enumerates existing non-symlink sensitive
// directories and regular files inside workDir so bubblewrap can mask them with
// empty tmpfs or read-only /dev/null overlays. It fails closed if any
// sensitive entry (or its parent) is a symbolic link or non-regular special file.
func collectSensitiveWorkspaceMasks(workDir string) (dirs []string, files []string, err error) {
	info, err := os.Lstat(workDir)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect workspace for sandbox masking: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, nil, fmt.Errorf("refusing sandbox execution: workspace root is not a real directory")
	}

	err = filepath.WalkDir(workDir, func(path string, d fs.DirEntry, walkErr error) error {
		relPath, relErr := filepath.Rel(workDir, path)
		if relErr != nil {
			return relErr
		}
		rel := filepath.ToSlash(relPath)
		if rel == "." {
			if walkErr != nil {
				return walkErr
			}
			return nil
		}
		if walkErr != nil {
			return fmt.Errorf("inspect %s for sandbox masking: %w", rel, walkErr)
		}

		// Never follow symlinks: WalkDir does not descend into symlinked dirs.
		// Fail closed if any sensitive path component itself is a symlink.
		if d.Type()&os.ModeSymlink != 0 {
			if isSensitivePath(rel) {
				return fmt.Errorf("refusing sandbox execution: sensitive path %s is a symbolic link", rel)
			}
			return nil
		}

		if !isSensitivePath(rel) {
			return nil
		}
		if len(dirs)+len(files) >= maxSandboxWorkspaceMasks {
			return fmt.Errorf("refusing sandbox execution: more than %d sensitive paths to mask cleanly", maxSandboxWorkspaceMasks)
		}
		if d.IsDir() {
			dirs = append(dirs, strings.TrimPrefix(rel, "./"))
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			files = append(files, strings.TrimPrefix(rel, "./"))
			return nil
		}
		return fmt.Errorf("refusing sandbox execution: sensitive path %s has unsupported file type", rel)
	})
	if err != nil {
		return nil, nil, err
	}

	sort.Strings(dirs)
	sort.Strings(files)
	return dirs, files, nil
}
