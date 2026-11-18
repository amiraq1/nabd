//go:build unix

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func makeFIFO(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
}

func runToolString(t *testing.T, fn func() string) string {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- fn() }()
	select {
	case out := <-done:
		return out
	case <-time.After(2 * time.Second):
		t.Fatal("tool call blocked on a special file")
		return ""
	}
}

func expectQuickError(t *testing.T, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call blocked on a special file")
	}
}

func TestReadFileToolRejectsFIFO(t *testing.T) {
	root, reg := newTestRegistry(t)
	makeFIFO(t, filepath.Join(root, "pipe"))

	out := runToolString(t, func() string {
		return reg.Run(context.Background(), "read_file", json.RawMessage(`{"path":"pipe"}`))
	})
	if !strings.HasPrefix(out, "Error") {
		t.Fatalf("read_file accepted FIFO: %.120q", out)
	}
}

func TestListDirToolRejectsFIFO(t *testing.T) {
	root, reg := newTestRegistry(t)
	makeFIFO(t, filepath.Join(root, "pipe"))

	out := runToolString(t, func() string {
		return reg.Run(context.Background(), "list_dir", json.RawMessage(`{"path":"pipe"}`))
	})
	if !strings.HasPrefix(out, "Error") {
		t.Fatalf("list_dir accepted FIFO: %.120q", out)
	}
}

func TestFileDigestRejectsFIFO(t *testing.T) {
	root, reg := newTestRegistry(t)
	makeFIFO(t, filepath.Join(root, "pipe"))

	expectQuickError(t, func() error {
		_, err := reg.FileDigest("pipe")
		return err
	})
}

func TestReadFileLimitedRejectsFIFO(t *testing.T) {
	root, reg := newTestRegistry(t)
	makeFIFO(t, filepath.Join(root, "pipe"))

	expectQuickError(t, func() error {
		_, err := reg.readFileLimited("pipe", 1024)
		return err
	})
}

func TestPreviewApprovalRejectsFIFO(t *testing.T) {
	root, reg := newTestRegistry(t)
	makeFIFO(t, filepath.Join(root, "pipe"))

	raw := json.RawMessage(`{"path":"pipe","old_text":"x","new_text":"y"}`)
	expectQuickError(t, func() error {
		_, err := reg.PreviewApproval("edit_file", raw)
		return err
	})
}

func TestEditToolRejectsFIFO(t *testing.T) {
	root := t.TempDir()
	approved := false
	reg, err := NewRegistry(root, func(string, string) bool {
		approved = true
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	makeFIFO(t, filepath.Join(root, "pipe"))

	raw := json.RawMessage(`{"path":"pipe","old_text":"x","new_text":"y"}`)
	out := runToolString(t, func() string {
		return reg.Run(context.Background(), "edit_file", raw)
	})
	if !strings.HasPrefix(out, "Error") {
		t.Fatalf("edit_file accepted FIFO: %.120q", out)
	}
	if approved {
		t.Fatal("edit_file asked for approval before rejecting a special file")
	}
}

func TestUndoRejectsFIFO(t *testing.T) {
	root, reg := newTestRegistry(t)
	target := filepath.Join(root, "target.txt")
	writeTestFile(t, target, "old\n")

	out := runToolApproved(t, reg, "edit_file", map[string]any{
		"path": "target.txt", "old_text": "old", "new_text": "new",
	})
	if !strings.Contains(out, "Successfully") {
		t.Fatalf("setup edit = %q", out)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	makeFIFO(t, target)

	expectQuickError(t, func() error {
		_, err := reg.Undo("target.txt")
		return err
	})
}
