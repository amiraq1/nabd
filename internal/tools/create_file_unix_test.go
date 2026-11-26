//go:build unix

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

func TestCreateFileRespectsUmaskWithoutExecuteBits(t *testing.T) {
	if dir := os.Getenv("NABD_CREATE_UMASK_DIR"); dir != "" {
		reg, err := NewRegistry(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer reg.Close()

		run := func(name string, umask int, wantPerm os.FileMode) {
			old := syscall.Umask(umask)
			defer syscall.Umask(old)

			content := "package main\n"
			raw, err := json.Marshal(map[string]any{"path": name, "content": content})
			if err != nil {
				t.Fatal(err)
			}
			p, err := reg.PreviewApproval("create_file", raw)
			if err != nil {
				t.Fatalf("PreviewApproval(%s): %v", name, err)
			}
			tok, err := reg.ApprovePreview("create_file", raw, p.State)
			if err != nil {
				t.Fatalf("ApprovePreview(%s): %v", name, err)
			}
			out := reg.RunApproved(context.Background(), "create_file", raw, tok)
			if !strings.HasPrefix(out, "Successfully created") {
				t.Fatalf("RunApproved(%s) = %q", name, out)
			}
			info, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o111 != 0 {
				t.Fatalf("%s has execute bit: %o", name, info.Mode().Perm())
			}
			if info.Mode().Perm() != wantPerm {
				t.Fatalf("%s mode = %04o, want %04o under umask %04o", name, info.Mode().Perm(), wantPerm, umask)
			}
		}

		run("strict.go", 0o077, 0o600)
		run("normal.go", 0o022, 0o644)
		return
	}

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCreateFileRespectsUmaskWithoutExecuteBits$")
	cmd.Env = append(os.Environ(), "NABD_CREATE_UMASK_DIR="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("umask child failed: %v\n%s", err, out)
	}
}
