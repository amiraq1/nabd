package shadow

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func storeSnapshot(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "missing"
		}
		t.Fatal(err)
	}
	hasBak, hasMeta := false, false
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), backupExt) {
			hasBak = true
		}
		if strings.HasSuffix(entry.Name(), metaSuffix) {
			hasMeta = true
		}
	}
	return fmt.Sprintf("bak=%t meta=%t", hasBak, hasMeta)
}

// TestBackupSyncsDataThenMetadata pins the durable ordering: the backup bytes
// and their directory entry are synced before metadata is renamed into place,
// and the rename is synced before Backup reports success.
func TestBackupSyncsDataThenMetadata(t *testing.T) {
	store, dir := openTestStore(t)
	var phases []string
	store.syncDir = func(*os.File) error {
		phases = append(phases, storeSnapshot(t, dir))
		return nil
	}

	if _, err := store.Backup("a.txt", []byte("before"), 0o600, []byte("after")); err != nil {
		t.Fatalf("backup failed: %v", err)
	}

	// The parent sync happens before the backup exists; the next sync must see
	// the data file without metadata, and a later one must see both.
	var sawDataOnly, sawBoth bool
	for _, phase := range phases {
		if phase == "bak=true meta=false" {
			sawDataOnly = true
			continue
		}
		if sawDataOnly && phase == "bak=true meta=true" {
			sawBoth = true
		}
	}
	if !sawDataOnly {
		t.Fatalf("no sync happened between writing the backup and publishing metadata: %v", phases)
	}
	if !sawBoth {
		t.Fatalf("metadata rename was not followed by a directory sync: %v", phases)
	}
	if phases[len(phases)-1] != "bak=true meta=true" {
		t.Fatalf("last sync saw an incomplete store: %v", phases)
	}
}

// TestBackupFailsClosedWhenRequiredSyncFails proves a failed sync before the
// metadata commit is reported and never produces a healthy-looking entry.
func TestBackupFailsClosedWhenRequiredSyncFails(t *testing.T) {
	store, dir := openTestStore(t)
	injected := errors.New("injected: directory sync denied")
	store.syncDir = func(*os.File) error {
		if storeSnapshot(t, dir) == "bak=true meta=false" {
			return injected
		}
		return nil
	}

	if _, err := store.Backup("a.txt", []byte("before"), 0o600, []byte("after")); !errors.Is(err, injected) {
		t.Fatalf("Backup = %v, want the injected sync failure", err)
	}
	if _, _, err := store.Latest("a.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Latest after a failed sync = %v, want ErrNotFound (no advertised entry)", err)
	}
	if got := storeSnapshot(t, dir); got != "bak=true meta=false" {
		t.Fatalf("store snapshot = %q, want only an orphaned backup", got)
	}

	// The orphan is reclaimable and a later backup with working sync succeeds.
	store.syncDir = nil
	store.now = func() time.Time { return time.Now().Add(2 * orphanGrace) }
	if _, err := store.Backup("b.txt", []byte("before"), 0o600, []byte("after")); err != nil {
		t.Fatalf("backup after the sync failure cleared: %v", err)
	}
}

// TestCleanupReportsDirectorySyncFailureAndRecovers keeps a cleanup sync error
// visible instead of silently declaring the store healthy.
func TestCleanupReportsDirectorySyncFailureAndRecovers(t *testing.T) {
	store, _ := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	entry, err := store.Backup("a.txt", []byte("before"), 0o600, []byte("after"))
	if err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected: cleanup directory sync denied")
	failing := true
	store.syncDir = func(*os.File) error {
		if failing {
			return injected
		}
		return nil
	}
	now := base.Add(retention + time.Hour)
	if err := store.Cleanup(now); !errors.Is(err, injected) {
		t.Fatalf("Cleanup = %v, want the injected sync failure", err)
	}
	if _, lastErr := store.LastCleanupError(); !errors.Is(lastErr, injected) {
		t.Fatalf("LastCleanupError = %v, want the injected failure", lastErr)
	}
	if _, err := os.Stat(store.root.Name() + string(os.PathSeparator) + backupName(entry.ID)); err == nil {
		t.Fatalf("data removal did not complete before the sync failure")
	}

	failing = false
	if err := store.Cleanup(now); err != nil {
		t.Fatalf("resumed cleanup: %v", err)
	}
	if _, lastErr := store.LastCleanupError(); lastErr != nil {
		t.Fatalf("failure stayed sticky after a successful sweep: %v", lastErr)
	}
	if _, _, err := store.Latest("a.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Latest after cleanup = %v, want ErrNotFound", err)
	}
}

// TestUnsupportedDirectorySyncStaysUsable documents the Windows policy: the
// platform limitation is reported through DirectorySyncSupported instead of
// turning a backup into a silent success or into a hard failure.
func TestUnsupportedDirectorySyncStaysUsable(t *testing.T) {
	store, _ := openTestStore(t)
	if store.DirectorySyncSupported() {
		t.Skip("platform supports directory fsync")
	}
	store.syncDir = func(*os.File) error { return ErrDirSyncUnsupported }
	if _, err := store.Backup("a.txt", []byte("before"), 0o600, []byte("after")); err != nil {
		t.Fatalf("backup on a platform without directory fsync failed: %v", err)
	}
}
