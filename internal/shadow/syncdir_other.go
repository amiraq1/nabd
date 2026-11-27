//go:build !unix

package shadow

import "os"

// syncDirectory cannot fsync a directory on these platforms. The caller maps
// ErrDirSyncUnsupported to "continue with a documented durability limitation"
// instead of silently claiming the directory entry is durable.
func syncDirectory(*os.File) error { return ErrDirSyncUnsupported }

// directorySyncSupported reports whether directory fsync is available.
const directorySyncSupported = false
