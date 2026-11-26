//go:build unix

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestAcquireProcessLockRejectsFIFO(t *testing.T) {
	dir := t.TempDir()
	r := mustRegistry(t, dir)
	defer r.Close()

	if err := os.MkdirAll(filepath.Join(dir, ".nabd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, ".nabd", "edit.lock"), 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	if _, err := acquireProcessLock(r.root); err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("expected non-regular edit.lock error for FIFO, got: %v", err)
	}
}
