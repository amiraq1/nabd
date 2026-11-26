//go:build unix

package safefs

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOpenFileNoFollowRejectsFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	fifoPath := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	if _, _, err := ReadFileNoFollow(fifoPath, OpenPolicy{MaxBytes: 4096}); !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("expected ErrNotRegularFile for FIFO, got %v", err)
	}
}
