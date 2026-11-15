//go:build linux

package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Run each potentially blocking operation in an isolated process so a broken
// implementation cannot strand the test runner or its goroutines.
func TestIntegrationFIFOSubprocess(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--" {
		t.Skip("fixture only")
	}
	op, dir := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	reg, err := NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	switch op {
	case "read":
		out := reg.Run(context.Background(), "read_file", json.RawMessage(`{"path":"pipe"}`))
		if !strings.HasPrefix(out, "Error:") {
			t.Fatal("FIFO accepted")
		}
	case "digest":
		if _, err := reg.FileDigest("pipe"); err == nil {
			t.Fatal("FIFO accepted")
		}
	case "preview":
		if _, err := reg.PreviewApproval("edit_file", json.RawMessage(`{"path":"pipe","old_text":"old","new_text":"new"}`)); err == nil {
			t.Fatal("FIFO accepted")
		}
	case "bounded":
		if _, err := readBounded(reg.root, "pipe", maxEditBytes); err == nil {
			t.Fatal("FIFO accepted")
		}
	case "list":
		if _, err := reg.listDir("pipe"); err == nil {
			t.Fatal("FIFO accepted")
		}
	case "swapped":
		path := filepath.Join(dir, "swap-target")
		if err := os.WriteFile(path, []byte("regular"), 0600); err != nil {
			t.Fatal(err)
		}
		f, err := openRegularSingleLinkWith(reg.root, "swap-target", func(root *os.Root, rel string) (*os.File, error) {
			if err := os.Remove(path); err != nil {
				return nil, err
			}
			if err := syscall.Mkfifo(path, 0600); err != nil {
				return nil, err
			}
			return openWorkspaceFile(root, rel)
		})
		if f != nil {
			_ = f.Close()
		}
		if err == nil {
			t.Fatal("swapped FIFO accepted after Stat")
		}
	default:
		t.Fatal("unknown fixture operation")
	}
}

func TestIntegrationFIFORejectedWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"read", "digest", "preview", "bounded", "list", "swapped"} {
		t.Run(op, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIntegrationFIFOSubprocess$", "--", op, dir)
			if err := cmd.Run(); err != nil {
				if ctx.Err() != nil {
					t.Fatal("operation blocked on FIFO")
				}
				t.Fatalf("FIFO fixture failed: %v", err)
			}
		})
	}
}

func TestIntegrationHardLinkRejected(t *testing.T) {
	root, reg := newTestRegistry(t)
	outside := filepath.Join(t.TempDir(), "original")
	if err := os.WriteFile(outside, []byte("synthetic test content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(root, "alias")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	out := reg.Run(context.Background(), "read_file", json.RawMessage(`{"path":"alias"}`))
	if !strings.HasPrefix(out, "Error:") {
		t.Error("read_file accepted a hard link")
	}
	if _, err := reg.FileDigest("alias"); err == nil {
		t.Error("digest accepted a hard link")
	}
	if _, err := readBounded(reg.root, "alias", maxEditBytes); err == nil {
		t.Error("bounded read accepted a hard link")
	}
	if _, err := reg.PreviewApproval("edit_file", json.RawMessage(`{"path":"alias","old_text":"synthetic","new_text":"replaced"}`)); err == nil {
		t.Error("preview accepted a hard link")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "synthetic test content" {
		t.Fatal("original file changed")
	}
}
