//go:build !unix

package audit

import "os"

// syncDirectory is unavailable on these platforms. Directory fsync is not part
// of their file API, so creating audit.log is not guaranteed to survive a
// sudden power loss; the append itself is still fsynced. This limitation is
// documented in docs/THREAT_MODEL.md.
func syncDirectory(*os.File) error { return nil }
