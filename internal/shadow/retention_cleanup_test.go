package shadow

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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

func TestStopRetentionIsIdempotentConcurrentAndWaitsForExit(t *testing.T) {
	var nilStore *Store
	nilStore.StopRetention()

	store, _ := openTestStore(t)
	store.StopRetention()

	inCleanup := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var calls int32
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(inCleanup)
			<-releaseCleanup
		}
		return base
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.StartRetention(ctx, time.Hour)
	done := store.retentionDone
	<-inCleanup

	const callers = 8
	var stopReturned int32
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			store.StopRetention()
			atomic.AddInt32(&stopReturned, 1)
		}()
	}

	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt32(&stopReturned); n != 0 {
		close(releaseCleanup)
		wg.Wait()
		t.Fatalf("%d StopRetention callers returned before retention goroutine exited", n)
	}

	close(releaseCleanup)
	wg.Wait()

	select {
	case <-done:
	default:
		t.Fatal("retentionDone channel not closed after StopRetention returned")
	}

	store.StopRetention()
	store.StopRetention()
}
