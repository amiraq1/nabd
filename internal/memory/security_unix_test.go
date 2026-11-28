//go:build unix

package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestUnixRejectsFIFOAndHardLinksAndInnerSymlinks(t *testing.T) {
	ctx := context.Background()

	t.Run("fifo_lock", func(t *testing.T) {
		dir, s := openTestStore(t)
		memDir := filepath.Join(dir, ".nabd", "memory")
		if err := os.MkdirAll(memDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(filepath.Join(memDir, lockFileName), 0o600); err != nil {
			t.Skipf("mkfifo unsupported: %v", err)
		}
		if _, err := s.Save(ctx, SaveInput{Title: "FIFO lock", Body: "Must fail"}); err == nil {
			t.Fatal("accepted FIFO at memory.lock")
		}
	})

	t.Run("fifo_entries", func(t *testing.T) {
		dir, s := openTestStore(t)
		memDir := filepath.Join(dir, ".nabd", "memory")
		if err := os.MkdirAll(memDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(filepath.Join(memDir, entriesFileName), 0o600); err != nil {
			t.Skipf("mkfifo unsupported: %v", err)
		}
		if _, _, err := s.List(ctx, 5); err == nil {
			t.Fatal("accepted FIFO at entries.jsonl")
		}
	})

	t.Run("symlink_entries_and_memory_dir", func(t *testing.T) {
		dir, s := openTestStore(t)
		outside := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".nabd"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, ".nabd", "memory")); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
		if _, err := s.Save(ctx, SaveInput{Title: "Symlink memdir", Body: "Must fail"}); err == nil {
			t.Fatal("accepted symlinked .nabd/memory")
		}
	})

	t.Run("hardlink_entries", func(t *testing.T) {
		dir, s := openTestStore(t)
		if _, err := s.Save(ctx, SaveInput{Title: "Seed", Body: "Entry"}); err != nil {
			t.Fatal(err)
		}
		entriesPath := filepath.Join(dir, ".nabd", "memory", entriesFileName)
		extraLink := filepath.Join(dir, "extra-hardlink")
		if err := os.Link(entriesPath, extraLink); err != nil {
			t.Skipf("hardlinks unsupported: %v", err)
		}
		if _, _, err := s.List(ctx, 5); err == nil {
			t.Fatal("accepted multiply-linked entries.jsonl")
		}
	})
}
