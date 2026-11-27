//go:build unix

package audit

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAuditLockRejectsFIFO(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.MkdirAll(filepath.Join(dir, ".nabd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, ".nabd", lockName), 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	if _, err := acquireAuditLock(root, 100*time.Millisecond); err == nil {
		t.Fatal("expected a FIFO at audit.lock to be rejected without blocking")
	}
}
