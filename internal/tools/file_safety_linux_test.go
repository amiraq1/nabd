//go:build linux

package tools

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestFileDigestRejectsFIFOWithoutBlocking(t *testing.T) {
	root, reg := newTestRegistry(t)
	path := filepath.Join(root, "blocked.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.FileDigest("blocked.fifo"); err == nil {
		t.Fatal("FileDigest accepted a FIFO; want non-regular-file refusal")
	}
}
