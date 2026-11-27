//go:build unix

package shadow

import "os"

// syncDirectory makes directory entries (created backup files, renamed
// metadata) durable. On Unix a directory handle supports fsync.
func syncDirectory(dir *os.File) error { return dir.Sync() }

// directorySyncSupported reports whether directory fsync is available.
const directorySyncSupported = true
