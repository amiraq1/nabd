package shadow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeStoreFile(t *testing.T, dir, name string, data []byte, mod time.Time) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if !mod.IsZero() {
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// TestCleanupKeepsMetadataWhenBackupRemovalFails proves the sweep deletes data
// before metadata: a failing data removal leaves a recognizable entry that is
// retried later instead of an orphaned backup no metadata points at.
func TestCleanupKeepsMetadataWhenBackupRemovalFails(t *testing.T) {
	store, dir := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	entry, err := store.Backup("a.txt", []byte("before"), 0o600, []byte("after"))
	if err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected: remove backup denied")
	store.removeFile = func(name string) error {
		if strings.HasSuffix(name, backupExt) {
			return injected
		}
		return store.root.Remove(name)
	}

	now := base.Add(retention + time.Hour)
	if err := store.Cleanup(now); !errors.Is(err, injected) {
		t.Fatalf("Cleanup error = %v, want the injected failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, entry.ID+backupExt)); statErr != nil {
		t.Fatalf("backup must survive a failed deletion: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, entry.ID+metaSuffix)); statErr != nil {
		t.Fatalf("metadata must stay so the entry remains recognizable: %v", statErr)
	}
	if _, _, err := store.Latest("a.txt"); err != nil {
		t.Fatalf("entry should stay restorable while deletion keeps failing: %v", err)
	}

	store.removeFile = nil
	if err := store.Cleanup(now); err != nil {
		t.Fatalf("resumed cleanup failed: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, entry.ID+backupExt)); !os.IsNotExist(statErr) {
		t.Fatalf("backup was not removed after the failure cleared: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, entry.ID+metaSuffix)); !os.IsNotExist(statErr) {
		t.Fatalf("metadata was not removed after the failure cleared: %v", statErr)
	}
	if _, _, err := store.Latest("a.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Latest after a full deletion = %v, want ErrNotFound", err)
	}
}

// TestCleanupRepairsMetadataWithoutBackup covers a sweep interrupted after the
// data was removed: the leftover metadata is a tombstone and must be dropped
// instead of being advertised as a restorable entry.
func TestCleanupRepairsMetadataWithoutBackup(t *testing.T) {
	store, dir := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	entry, err := store.Backup("a.txt", []byte("before"), 0o600, []byte("after"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, entry.ID+backupExt)); err != nil {
		t.Fatal(err)
	}

	if err := store.Cleanup(base.Add(time.Minute)); err != nil {
		t.Fatalf("cleanup must repair an interrupted deletion: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, entry.ID+metaSuffix)); !os.IsNotExist(statErr) {
		t.Fatalf("tombstone metadata was not removed: %v", statErr)
	}
	if _, _, err := store.Latest("a.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Latest after repair = %v, want ErrNotFound", err)
	}
}

// TestLatestSkipsUnusableNewestEntry ensures a damaged newest copy is never
// returned as healthy, while an older verified copy stays restorable.
func TestLatestSkipsUnusableNewestEntry(t *testing.T) {
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
	if err := os.Remove(filepath.Join(dir, second.ID+backupExt)); err != nil {
		t.Fatal(err)
	}

	entry, data, err := store.Latest("a.txt")
	if err != nil {
		t.Fatalf("Latest must fall back to an intact entry: %v", err)
	}
	if entry.ID != first.ID {
		t.Fatalf("Latest returned %s, want the intact entry %s", entry.ID, first.ID)
	}
	if string(data) != "old-1" {
		t.Fatalf("Latest returned %q, want the verified bytes", data)
	}
}

// TestLatestFailsWhenNoEntryIsIntact keeps the fail-closed corner: when every
// candidate is unusable, callers get an explicit error, never a silent no-op.
func TestLatestFailsWhenNoEntryIsIntact(t *testing.T) {
	store, dir := openTestStore(t)
	entry, err := store.Backup("a.txt", []byte("only"), 0o600, []byte("after"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, entry.ID+backupExt)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Latest("a.txt"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Latest = %v, want an explicit integrity failure", err)
	}
}

// TestCleanupSweepsOrphanedBackupAndTempFiles covers data written without
// metadata and a metadata write interrupted mid-flight.
func TestCleanupSweepsOrphanedBackupAndTempFiles(t *testing.T) {
	store, dir := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }

	old := base.Add(-2 * orphanGrace)
	orphanBak := writeStoreFile(t, dir, "20251201T000000.000000000-orphan"+backupExt, []byte("data"), old)
	orphanTmp := writeStoreFile(t, dir, metaTmpPrefix+"deadbeefdeadbeef", []byte("partial"), old)

	if err := store.Cleanup(base); err != nil {
		t.Fatalf("cleanup with orphans failed: %v", err)
	}
	for _, path := range []string{orphanBak, orphanTmp} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("orphan %s survived the sweep: %v", filepath.Base(path), err)
		}
	}
}

// TestCleanupNeverTouchesFilesOutsideTheStore guards the sweep's path scope: a
// file that merely looks like a store entry must stay untouched in the
// workspace root.
func TestCleanupNeverTouchesFilesOutsideTheStore(t *testing.T) {
	store, dir := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }

	workspaceRoot := filepath.Dir(filepath.Dir(dir))
	outside := writeStoreFile(t, workspaceRoot, "20251201T000000.000000000-outsider"+backupExt, []byte("keep me"), base.Add(-2*orphanGrace))

	if err := store.Cleanup(base); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("cleanup removed a file outside the shadow store: %v", err)
	}
}

// TestCleanupKeepsRecentStoreFilesWithinGrace avoids racing a Backup that is
// still writing metadata in another process.
func TestCleanupKeepsRecentStoreFilesWithinGrace(t *testing.T) {
	store, dir := openTestStore(t)
	now := time.Now()
	recent := writeStoreFile(t, dir, metaTmpPrefix+"stillwriting", []byte("partial"), now.Add(-time.Second))

	if err := store.Cleanup(now); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("a recent store file was deleted inside the grace period: %v", err)
	}
}

// TestCleanupFailureIsReportedAndCleared pins the reporting contract: one
// failure stays visible until a later sweep fully succeeds.
func TestCleanupFailureIsReportedAndCleared(t *testing.T) {
	store, _ := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	if _, err := store.Backup("a.txt", []byte("before"), 0o600, []byte("after")); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected denial")
	store.removeFile = func(name string) error {
		if strings.HasSuffix(name, backupExt) {
			return injected
		}
		return store.root.Remove(name)
	}
	now := base.Add(retention + time.Hour)
	if err := store.Cleanup(now); err == nil {
		t.Fatal("expected a cleanup failure")
	}

	lastErr, at := store.LastCleanupError()
	if !errors.Is(lastErr, injected) {
		t.Fatalf("LastCleanupError = %v, want the injected failure", lastErr)
	}
	if !at.Equal(now) {
		t.Fatalf("LastCleanupError time = %s, want %s", at, now)
	}
	if failures, _ := store.CleanupFailures(); failures != 1 {
		t.Fatalf("CleanupFailures = %d, want 1", failures)
	}

	store.removeFile = nil
	if err := store.Cleanup(now); err != nil {
		t.Fatalf("cleanup after clearing the failure failed: %v", err)
	}
	if lastErr, _ := store.LastCleanupError(); lastErr != nil {
		t.Fatalf("failure stayed sticky after a successful sweep: %v", lastErr)
	}
	if failures, success := store.CleanupFailures(); failures != 0 || success.IsZero() {
		t.Fatalf("CleanupFailures = %d, lastSuccess = %s; want 0 and a timestamp", failures, success)
	}
}

// TestBackupReportsCleanupFailureWithoutFailing separates a required backup
// failure from a later cleanup failure: a successful edit must not be reported
// as failed because an old copy could not be deleted.
func TestBackupReportsCleanupFailureWithoutFailing(t *testing.T) {
	store, _ := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	if _, err := store.Backup("a.txt", []byte("before"), 0o600, []byte("after")); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected denial")
	store.removeFile = func(name string) error {
		if strings.HasSuffix(name, backupExt) {
			return injected
		}
		return store.root.Remove(name)
	}
	store.now = func() time.Time { return base.Add(retention + time.Hour) }

	entry, err := store.Backup("a.txt", []byte("before-2"), 0o600, []byte("after-2"))
	if err != nil {
		t.Fatalf("a cleanup failure must not fail a successful backup: %v", err)
	}
	if entry.ID == "" {
		t.Fatal("backup returned no entry")
	}
	got, data, err := store.Latest("a.txt")
	if err != nil {
		t.Fatalf("new backup is not restorable: %v", err)
	}
	if got.ID != entry.ID || string(data) != "before-2" {
		t.Fatalf("Latest = %+v %q, want the new entry", got, data)
	}
	if lastErr, _ := store.LastCleanupError(); !errors.Is(lastErr, injected) {
		t.Fatalf("cleanup failure was not reported: %v", lastErr)
	}
}

// TestBackupSweepsStaleOrphansBeforeWriting keeps leftovers from blocking a
// backup: the sweep runs before the quota check, not only after the write.
func TestBackupSweepsStaleOrphansBeforeWriting(t *testing.T) {
	store, dir := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }

	stale := writeStoreFile(t, dir, "20251201T000000.000000000-stale"+backupExt, []byte("leftover"), base.Add(-2*orphanGrace))

	if _, err := store.Backup("a.txt", []byte("before"), 0o600, []byte("after")); err != nil {
		t.Fatalf("backup failed: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale orphan survived the pre-write sweep: %v", err)
	}
}

// TestRetentionBackupAndReportingRunConcurrently is the -race target for the
// shared state behind LastCleanupError.
func TestRetentionBackupAndReportingRunConcurrently(t *testing.T) {
	store, _ := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.StartRetention(ctx, time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if _, err := store.Backup(fmt.Sprintf("f%d.txt", worker), []byte("before"), 0o600, []byte("after")); err != nil {
					t.Errorf("worker %d backup: %v", worker, err)
					return
				}
				_, _ = store.LastCleanupError()
				_, _ = store.CleanupFailures()
			}
		}(i)
	}
	wg.Wait()
	store.StopRetention()
}
