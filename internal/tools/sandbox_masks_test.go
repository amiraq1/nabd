package tools

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCollectSensitiveWorkspaceMasksIncludesSecretsAndSkipsTemplates(t *testing.T) {
	dir := t.TempDir()

	// Sensitive directories and files
	if err := os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("secret-remote"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".nabd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".nabd", "audit.log"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "server.key"), []byte("privkey"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "id_ed25519"), []byte("ssh-priv"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Non-sensitive files & exempt templates
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte("PUBLIC=0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "id_ed25519.pub"), []byte("ssh-pub"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirs, files, err := collectSensitiveWorkspaceMasks(dir)
	if err != nil {
		t.Fatalf("collectSensitiveWorkspaceMasks failed: %v", err)
	}
	wantDirs := []string{".git", ".nabd"}
	if !reflect.DeepEqual(dirs, wantDirs) {
		t.Fatalf("dirs = %v, want %v", dirs, wantDirs)
	}
	wantFiles := []string{".env", "config/server.key", "id_ed25519"}
	if !reflect.DeepEqual(files, wantFiles) {
		t.Fatalf("files = %v, want %v", files, wantFiles)
	}
}

func TestCollectSensitiveWorkspaceMasksSupportsWorktreeGitFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirs, files, err := collectSensitiveWorkspaceMasks(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dirs) != 0 {
		t.Fatalf("expected 0 dirs when .git is regular worktree file, got %v", dirs)
	}
	if !reflect.DeepEqual(files, []string{".git"}) {
		t.Fatalf("files = %v, want [.git]", files)
	}
}

func TestCollectSensitiveWorkspaceMasksRejectsSensitiveSymlinksAndFIFOs(t *testing.T) {
	t.Run("symlinked_env", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(t.TempDir(), "real.txt")
		if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, ".env")); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if _, _, err := collectSensitiveWorkspaceMasks(dir); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("expected symbolic link error, got: %v", err)
		}
	})

	t.Run("symlinked_nabd_dir", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Symlink(t.TempDir(), filepath.Join(dir, ".nabd")); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if _, _, err := collectSensitiveWorkspaceMasks(dir); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("expected symbolic link error, got: %v", err)
		}
	})
}
