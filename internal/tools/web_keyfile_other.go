//go:build !unix && !windows

package tools

import "os"

// validateWebKeyFilePlatform enforces the hard-link check on non-Unix,
// non-Windows platforms after safefs opening.
func validateWebKeyFilePlatform(f *os.File, _ string) error {
	return ensureNoHardLinks(f)
}
