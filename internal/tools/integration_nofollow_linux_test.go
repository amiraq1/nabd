//go:build linux

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationLinuxRejectsFileAndParentSymlinks(t *testing.T) {
	root, r := newTestRegistry(t)
	writeTestFile(t, filepath.Join(root, "real", "file"), "fixture")
	if err := os.Symlink("real/file", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(root, "diralias")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"alias", "diralias/file"} {
		if out := runTool(t, r, "read_file", map[string]any{"path": path}); !strings.HasPrefix(out, "Error:") {
			t.Errorf("read followed symlink %s", path)
		}
		if _, err := r.FileDigest(path); err == nil {
			t.Errorf("digest followed symlink %s", path)
		}
	}
	if out := runTool(t, r, "list_dir", map[string]any{"path": "diralias"}); !strings.HasPrefix(out, "Error:") {
		t.Error("list_dir followed symlink")
	}
	if out := runTool(t, r, "read_file", map[string]any{"path": "real/file"}); out != "fixture" {
		t.Error("normal file unavailable")
	}
}
func TestIntegrationLinuxSymlinkSwapAfterPathValidation(t *testing.T) {
	root, r := newTestRegistry(t)
	writeTestFile(t, filepath.Join(root, "file"), "safe")
	writeTestFile(t, filepath.Join(root, ".git", "config"), "synthetic")
	rel, err := r.resolveSafePath("file")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git/config", filepath.Join(root, "file")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.readFilePage(rel, 0, 16, false); err == nil {
		t.Error("read followed sensitive symlink introduced after validation")
	}
}

func TestIntegrationLinuxOpenRejectsTraversal(t *testing.T) {
	base := t.TempDir()
	rootDir := filepath.Join(base, "root")
	if err := os.Mkdir(rootDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(base, "outside"), "fixture")
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := openWorkspaceFile(root, "../outside")
	if f != nil {
		_ = f.Close()
	}
	if err == nil {
		t.Fatal("direct opener accepted traversal")
	}
}
