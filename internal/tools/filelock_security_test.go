package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireProcessLockRejectsSymlinksAndFIFOs(t *testing.T) {
	t.Run("symlinked_nabd_dir", func(t *testing.T) {
		dir := t.TempDir()
		r := mustRegistry(t, dir)
		defer r.Close()

		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(dir, ".nabd")); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if _, err := acquireProcessLock(r.root); err == nil {
			t.Fatal("expected symlinked .nabd lock directory to be rejected")
		}
		if _, err := os.Stat(filepath.Join(outside, "edit.lock")); !os.IsNotExist(err) {
			t.Fatalf("acquireProcessLock created edit.lock outside workspace: err=%v", err)
		}
	})

	t.Run("symlinked_edit_lock", func(t *testing.T) {
		dir := t.TempDir()
		r := mustRegistry(t, dir)
		defer r.Close()

		if err := os.MkdirAll(filepath.Join(dir, ".nabd"), 0o700); err != nil {
			t.Fatal(err)
		}
		outsideTarget := filepath.Join(t.TempDir(), "target.txt")
		if err := os.WriteFile(outsideTarget, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outsideTarget, filepath.Join(dir, ".nabd", "edit.lock")); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if _, err := acquireProcessLock(r.root); err == nil {
			t.Fatal("expected symlinked edit.lock to be rejected")
		}
	})

	t.Run("dir_as_edit_lock", func(t *testing.T) {
		dir := t.TempDir()
		r := mustRegistry(t, dir)
		defer r.Close()

		if err := os.MkdirAll(filepath.Join(dir, ".nabd", "edit.lock"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := acquireProcessLock(r.root); err == nil {
			t.Fatal("expected directory at edit.lock to be rejected")
		}
	})
}
