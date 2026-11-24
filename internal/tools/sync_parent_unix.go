//go:build unix

package tools

import "os"

func syncParentDirectory(dir *os.File) error { return dir.Sync() }
