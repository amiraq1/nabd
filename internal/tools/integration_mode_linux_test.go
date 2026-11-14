//go:build linux

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestIntegrationModeFixture(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-1] != "mode-fixture" {
		t.Skip("fixture only")
	}
	dir, r := newTestRegistry(t)
	path := filepath.Join(dir, "target.txt")
	writeTestFile(t, path, "before")
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	old := syscall.Umask(0077)
	defer syscall.Umask(old)
	out := runToolApproved(t, r, "edit_file", map[string]any{"path": "target.txt", "old_text": "before", "new_text": "after"})
	if !strings.HasPrefix(out, "Successfully updated") {
		t.Fatalf("setup edit failed: %s", out)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0640 {
		t.Fatal("edit failed to preserve mode under restrictive umask")
	}
	if _, err := r.Undo("target.txt"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0640 {
		t.Fatal("undo failed to preserve mode under restrictive umask")
	}
}
func TestIntegrationEditUndoPreserveModeUnderUmask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIntegrationModeFixture$", "--", "mode-fixture")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mode fixture failed: %v\n%s", err, output)
	}
}
