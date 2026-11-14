//go:build !unix

package audit

import "os"

// File.Sync remains in use on platforms without a portable directory fsync.
func syncDirectory(_ *os.Root, _ string) error { return nil }
