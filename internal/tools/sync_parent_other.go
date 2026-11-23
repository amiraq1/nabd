//go:build !unix

package tools

import "os"

// Directory sync is not provided by this implementation on these platforms.
func syncParentDirectory(_ *os.File) error { return nil }
