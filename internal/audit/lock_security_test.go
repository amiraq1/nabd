package audit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditLockRejectsSymlinksAndNonRegularFiles(t *testing.T) {
	t.Run("symlinked_nabd_dir", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()

		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(dir, ".nabd")); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if _, err := acquireAuditLock(root, 100*time.Millisecond); err == nil {
			t.Fatal("expected a symlinked .nabd to be rejected")
		}
		if _, err := os.Stat(filepath.Join(outside, lockName)); !os.IsNotExist(err) {
			t.Fatalf("lock was created outside the workspace: %v", err)
		}
	})

	t.Run("symlinked_lock", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		if err := os.MkdirAll(filepath.Join(dir, ".nabd"), 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "target.txt")
		if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, ".nabd", lockName)); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if _, err := acquireAuditLock(root, 100*time.Millisecond); err == nil {
			t.Fatal("expected a symlinked audit lock to be rejected")
		}
		data, err := os.ReadFile(outside)
		if err != nil || string(data) != "keep" {
			t.Fatalf("lock target outside the workspace was modified: %q %v", data, err)
		}
	})

	t.Run("directory_as_lock", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		if err := os.MkdirAll(filepath.Join(dir, ".nabd", lockName), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := acquireAuditLock(root, 100*time.Millisecond); err == nil {
			t.Fatal("expected a directory at audit.lock to be rejected")
		}
	})
}
