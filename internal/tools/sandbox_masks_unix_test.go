//go:build unix

package tools

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCollectSensitiveWorkspaceMasksRejectsFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, ".env"), 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	if _, _, err := collectSensitiveWorkspaceMasks(dir); err == nil || !strings.Contains(err.Error(), "unsupported file type") {
		t.Fatalf("expected unsupported file type error, got: %v", err)
	}
}
