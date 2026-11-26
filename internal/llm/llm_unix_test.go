//go:build unix

package llm

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestReadAPIKeyFileRejectsFIFOWithoutBlocking(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "pipe_key")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	if _, err := readAPIKeyFile(fifo); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("expected FIFO non-regular rejection, got %v", err)
	}
}
