package shadow

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return store, filepath.Join(dir, baseDir)
}

func TestScheduledCleanupRemovesExpiredWithoutBreakingRecentRestore(t *testing.T) {
	store, dir := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	first, err := store.Backup("a.txt", []byte("old-1"), 0o600, []byte("new-1"))
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return base.Add(time.Hour) }
	second, err := store.Backup("a.txt", []byte("old-2"), 0o600, []byte("new-2"))
	if err != nil {
		t.Fatal(err)
	}

	// An expired entry is removed, while the recent one remains restorable.
	store.now = func() time.Time { return base.Add(retention + 30*time.Minute) }
	if err := store.Cleanup(store.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, first.ID+metaSuffix)); !os.IsNotExist(err) {
		t.Fatal("expired metadata was not deleted")
	}
	entry, data, err := store.Latest("a.txt")
	if err != nil {
		t.Fatalf("recent backup was lost: %v", err)
	}
	if entry.ID != second.ID || string(data) != "old-2" {
		t.Fatalf("wrong restore entry: %+v %q", entry, data)
	}
}

func TestRetentionLoopStopsAndIsCancellable(t *testing.T) {
	store, _ := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	store.StartRetention(ctx, 5*time.Millisecond)
	if store.retentionDone == nil {
		t.Fatal("retention loop was not started")
	}
	cancel()
	select {
	case <-store.retentionDone:
	case <-time.After(2 * time.Second):
		t.Fatal("retention loop did not stop on cancellation")
	}
	store.StopRetention()
}
