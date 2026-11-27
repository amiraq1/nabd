//go:build unix

package audit

import "os"

// syncDirectory makes a newly created log entry durable in its parent
// directory. Unix filesystems support fsync on a directory handle.
func syncDirectory(dir *os.File) error { return dir.Sync() }
