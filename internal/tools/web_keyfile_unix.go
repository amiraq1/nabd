//go:build unix

package tools

import "os"

// validateWebKeyFilePlatform enforces the Unix hard-link check on top of safefs
// descriptor checks (owner, 0600 mode, non-executable, symlink-free path).
func validateWebKeyFilePlatform(f *os.File, _ string) error {
	return ensureNoHardLinks(f)
}
