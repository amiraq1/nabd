// Package shadow stores pre-edit copies of files so an edit can be undone.
// The store lives under .nabd/shadow and is never exposed through the agent's
// file tools.
package shadow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	baseDir          = ".nabd/shadow"
	metaSuffix       = ".json"
	backupExt        = ".bak"
	metaTmpPrefix    = ".tmp-"
	maxPerPath       = 20
	retention        = 7 * 24 * time.Hour
	maxBackupBytes   = 2 << 20
	maxMetadataBytes = 16 << 10
	maxTotalBytes    = 128 << 20
	maxStoreEntries  = 10000
	// orphanGrace is how long an unreferenced store file must stay untouched
	// before a sweep deletes it. The store mutex only coordinates one process,
	// so the grace period leaves room for a Backup running elsewhere to finish
	// writing its metadata before its data file is treated as orphaned.
	orphanGrace = 15 * time.Minute
)

// ErrNotFound reports that no usable shadow entry exists for the request.
var ErrNotFound = errors.New("no shadow entry found")

// Entry describes one pre-edit backup.
type Entry struct {
	ID           string    `json:"id"`
	Path         string    `json:"path"`
	SHA256Before string    `json:"sha256_before"`
	SHA256After  string    `json:"sha256_after"`
	Mode         uint32    `json:"mode"`
	Size         int64     `json:"size"`
	CreatedAt    time.Time `json:"created_at"`
	Undone       bool      `json:"undone,omitempty"`
}

// Store persists backups inside an already-jailed workspace root.
type Store struct {
	root          *os.Root
	mu            sync.Mutex
	retentionMu   sync.Mutex
	now           func() time.Time
	retention     *retentionState
	retentionDone chan struct{}
	// removeFile is a test seam for injecting deletion failures. Production
	// leaves it nil so removals go straight to the confined root.
	removeFile func(name string) error
	cleanup    cleanupState
}

// cleanupState records the outcome of the most recent retention sweep so a
// failure is visible to the user instead of being silently discarded.
type cleanupState struct {
	err       error
	at        time.Time
	failures  int
	succeeded time.Time
}

type retentionState struct {
	stopOnce sync.Once
	stopCh   chan struct{}
	cancel   context.CancelFunc
	done     chan struct{}
	wg       sync.WaitGroup
}

func (r *retentionState) stop() {
	r.stopOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		close(r.stopCh)
	})
}

// Open creates a store bound to root. It does not write anything until the
// first Backup, so read-only workspaces can still be used for reading.
func Open(root *os.Root) (*Store, error) {
	if root == nil {
		return nil, errors.New("shadow: nil root")
	}
	return &Store{root: root, now: time.Now}, nil
}

// StartRetention runs cleanup immediately and then on the supplied interval.
// With the 7-day retention rule this gives a bounded deletion deadline while
// the process is running, instead of cleanup only on the next backup.
func (s *Store) StartRetention(ctx context.Context, interval time.Duration) {
	if s == nil || interval <= 0 {
		return
	}
	for {
		s.retentionMu.Lock()
		prev := s.retention
		if prev == nil {
			runCtx, cancel := context.WithCancel(ctx)
			state := &retentionState{
				stopCh: make(chan struct{}),
				cancel: cancel,
				done:   make(chan struct{}),
			}
			state.wg.Add(1)
			s.retention = state
			s.retentionDone = state.done
			s.retentionMu.Unlock()

			go s.runRetentionLoop(runCtx, interval, state)
			return
		}
		s.retentionMu.Unlock()

		prev.stop()
		prev.wg.Wait()

		s.retentionMu.Lock()
		if s.retention == prev {
			s.retention = nil
		}
		s.retentionMu.Unlock()
	}
}

func (s *Store) runRetentionLoop(runCtx context.Context, interval time.Duration, state *retentionState) {
	defer state.wg.Done()
	defer close(state.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		_ = s.Cleanup(s.now())
		select {
		case <-runCtx.Done():
			return
		case <-state.stopCh:
			return
		case <-ticker.C:
		}
	}
}

// StopRetention signals the background cleanup loop to stop and waits for it
// to exit. It is idempotent and safe for concurrent callers.
func (s *Store) StopRetention() {
	if s == nil {
		return
	}
	s.retentionMu.Lock()
	state := s.retention
	s.retentionMu.Unlock()
	if state == nil {
		return
	}
	state.stop()
	state.wg.Wait()
	s.retentionMu.Lock()
	if s.retention == state {
		s.retention = nil
	}
	s.retentionMu.Unlock()
}

// Cleanup applies the retention quota now and reports removal failures.
func (s *Store) Cleanup(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanupLocked(now)
}

// cleanupLocked runs one sweep and records its outcome.
func (s *Store) cleanupLocked(now time.Time) error {
	err := s.pruneLocked(now)
	s.cleanup.at = now
	if err != nil {
		s.cleanup.err = err
		s.cleanup.failures++
		return err
	}
	s.cleanup.err = nil
	s.cleanup.failures = 0
	s.cleanup.succeeded = now
	return nil
}

// LastCleanupError reports the most recent retention failure and the time of
// the sweep that produced it. A failure stays sticky until a later sweep
// succeeds, so one undeletable file cannot be mistaken for a healthy store.
// Messages reference store entries, never absolute host paths.
func (s *Store) LastCleanupError() (time.Time, error) {
	if s == nil {
		return time.Time{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanup.at, s.cleanup.err
}

// CleanupFailures reports how many consecutive sweeps failed and when the last
// fully successful sweep finished (zero time when none has).
func (s *Store) CleanupFailures() (int, time.Time) {
	if s == nil {
		return 0, time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanup.failures, s.cleanup.succeeded
}

// Backup saves the original bytes before an edit and records both hashes.
func (s *Store) Backup(relPath string, original []byte, mode os.FileMode, updated []byte) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.root.MkdirAll(baseDir, 0o700); err != nil {
		return Entry{}, fmt.Errorf("shadow: create directory: %w", err)
	}

	now := s.now().UTC()
	// Reclaim expired and orphaned files before the quota check: otherwise
	// leftovers from an interrupted sweep keep the store over quota and block
	// every later backup. A sweep failure is recorded, not fatal to the backup.
	_ = s.cleanupLocked(now)

	entry := Entry{
		ID:           now.Format("20060102T150405.000000000") + "-" + randomHex(4),
		Path:         relPath,
		SHA256Before: hashBytes(original),
		SHA256After:  hashBytes(updated),
		Mode:         uint32(mode.Perm()),
		Size:         int64(len(original)),
		CreatedAt:    now,
	}

	if len(original) > maxBackupBytes {
		return Entry{}, fmt.Errorf("shadow: backup too large")
	}
	meta, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return Entry{}, fmt.Errorf("shadow: encode metadata: %w", err)
	}
	if len(meta) > maxMetadataBytes {
		return Entry{}, fmt.Errorf("shadow: metadata too large")
	}
	used, err := s.diskUsageLocked()
	if err != nil {
		return Entry{}, err
	}
	if used+int64(len(original))+int64(len(meta)) > maxTotalBytes {
		return Entry{}, fmt.Errorf("shadow: total backup quota exceeded")
	}

	if err := s.writeNewLocked(backupName(entry.ID), original); err != nil {
		_ = s.root.Remove(backupName(entry.ID))
		return Entry{}, fmt.Errorf("shadow: write backup: %w", err)
	}
	if err := s.writeMetaLocked(entry); err != nil {
		_ = s.root.Remove(backupName(entry.ID))
		return Entry{}, err
	}

	_ = s.cleanupLocked(now)
	return entry, nil
}

// Latest returns the most recent usable entry, optionally restricted to one
// relative path ("" means any path), together with the original bytes.
func (s *Store) Latest(relPath string) (Entry, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.listLocked()
	if err != nil {
		return Entry{}, nil, err
	}

	var matches []Entry
	for _, entry := range entries {
		if entry.Undone {
			continue
		}
		if relPath != "" && entry.Path != relPath {
			continue
		}
		matches = append(matches, entry)
	}
	if len(matches) == 0 {
		return Entry{}, nil, ErrNotFound
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].CreatedAt.Equal(matches[j].CreatedAt) {
			return matches[i].ID > matches[j].ID
		}
		return matches[i].CreatedAt.After(matches[j].CreatedAt)
	})

	// Walk newest first and return the first entry whose bytes can actually be
	// read and verified. A missing, unreadable or corrupted backup is never
	// reported as a healthy restore target; it is skipped and, when nothing
	// usable remains, surfaced as an explicit failure.
	var unusable []error
	for _, entry := range matches {
		data, readErr := s.readLimited(backupName(entry.ID), maxBackupBytes)
		if readErr != nil {
			unusable = append(unusable, fmt.Errorf("shadow: read backup %s: %w", entry.ID, readErr))
			continue
		}
		if hashBytes(data) != entry.SHA256Before {
			unusable = append(unusable, fmt.Errorf("shadow: backup %s is corrupted", entry.ID))
			continue
		}
		return entry, data, nil
	}
	if len(unusable) == 0 {
		return Entry{}, nil, ErrNotFound
	}
	return Entry{}, nil, fmt.Errorf("shadow: no intact entry for %q (%d unusable): %w", relPath, len(unusable), errors.Join(unusable...))
}

// MarkUndone records that an entry has been restored.
func (s *Store) MarkUndone(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, err := s.readMetaLocked(id)
	if err != nil {
		return err
	}
	entry.Undone = true
	return s.writeMetaLocked(entry)
}

func (s *Store) writeMetaLocked(entry Entry) error {
	meta, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("shadow: encode metadata: %w", err)
	}
	tmp := baseDir + "/" + metaTmpPrefix + randomHex(8)
	if err := s.writeNewLocked(tmp, meta); err != nil {
		_ = s.root.Remove(tmp)
		return fmt.Errorf("shadow: write metadata: %w", err)
	}
	if err := s.root.Rename(tmp, metaName(entry.ID)); err != nil {
		_ = s.root.Remove(tmp)
		return fmt.Errorf("shadow: replace metadata: %w", err)
	}
	return nil
}

func (s *Store) writeNewLocked(name string, data []byte) error {
	f, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

func (s *Store) listLocked() ([]Entry, error) {
	f, err := s.root.Open(baseDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("shadow: open directory: %w", err)
	}
	defer f.Close()

	var entries []Entry
	count := 0
	for {
		dirEntries, readErr := f.ReadDir(128)
		for _, dirEntry := range dirEntries {
			count++
			if count > maxStoreEntries {
				return nil, fmt.Errorf("shadow: directory scan entry limit exceeded")
			}
			if dirEntry.IsDir() || !strings.HasSuffix(dirEntry.Name(), metaSuffix) {
				continue
			}
			entry, err := s.readMetaLocked(strings.TrimSuffix(dirEntry.Name(), metaSuffix))
			if err == nil {
				entries = append(entries, entry)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("shadow: read directory: %w", readErr)
		}
		if len(dirEntries) == 0 {
			break
		}
	}
	return entries, nil
}

// diskUsageLocked includes orphaned files and fails closed on unexpected types.
// The mutex protects writers in this Store; other processes are not coordinated.
func (s *Store) diskUsageLocked() (int64, error) {
	f, err := s.root.Open(baseDir)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var total int64
	count := 0
	for {
		entries, readErr := f.ReadDir(128)
		for _, entry := range entries {
			count++
			if count > maxStoreEntries {
				return 0, fmt.Errorf("shadow: quota scan entry limit exceeded")
			}
			info, err := entry.Info()
			if err != nil {
				return 0, err
			}
			if !info.Mode().IsRegular() {
				return 0, fmt.Errorf("shadow: unexpected non-regular store entry")
			}
			total += info.Size()
			if total > maxTotalBytes {
				return 0, fmt.Errorf("shadow: total backup quota exceeded")
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return 0, readErr
		}
		if len(entries) == 0 {
			return total, nil
		}
	}
}
func (s *Store) readLimited(name string, limit int64) ([]byte, error) {
	f, err := s.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("shadow: file too large: %s", name)
	}
	return data, nil
}

func (s *Store) readMetaLocked(id string) (Entry, error) {
	data, err := s.readLimited(metaName(id), maxMetadataBytes)
	if err != nil {
		return Entry{}, fmt.Errorf("shadow: read metadata: %w", err)
	}
	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return Entry{}, fmt.Errorf("shadow: decode metadata: %w", err)
	}
	if entry.ID != id {
		return Entry{}, fmt.Errorf("shadow: metadata id mismatch")
	}
	return entry, nil
}

// pruneLocked applies retention and repairs the store after interrupted
// deletions. Data is removed before metadata so an interrupted sweep leaves a
// recognizable tombstone (metadata whose backup is gone) instead of data that
// no metadata points at; the next sweep finishes it. Every step is idempotent,
// so running the sweep again is always safe.
func (s *Store) pruneLocked(now time.Time) error {
	entries, err := s.listLocked()
	if err != nil {
		return err
	}
	var cleanupErr error

	// Pass 1: finish deletions interrupted after the data was removed.
	live := make(map[string]struct{}, len(entries))
	remaining := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		live[entry.ID] = struct{}{}
		info, statErr := s.root.Lstat(backupName(entry.ID))
		switch {
		case statErr == nil && info.Mode().IsRegular():
			remaining = append(remaining, entry)
			continue
		case statErr == nil:
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("shadow: backup for entry %s is not a regular file", entry.ID))
			remaining = append(remaining, entry)
			continue
		case !errors.Is(statErr, os.ErrNotExist):
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("shadow: inspect backup for entry %s: %w", entry.ID, statErr))
			remaining = append(remaining, entry)
			continue
		}
		// The backup is already gone: drop the leftover metadata so the store
		// does not keep advertising an entry that cannot be restored.
		if err := s.removeLocked(metaName(entry.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("shadow: remove metadata for entry %s: %w", entry.ID, err))
		}
	}

	// Pass 2: apply the per-path retention quota, oldest first.
	byPath := make(map[string][]Entry)
	for _, entry := range remaining {
		byPath[entry.Path] = append(byPath[entry.Path], entry)
	}
	for _, group := range byPath {
		sort.Slice(group, func(i, j int) bool {
			if group[i].CreatedAt.Equal(group[j].CreatedAt) {
				return group[i].ID > group[j].ID
			}
			return group[i].CreatedAt.After(group[j].CreatedAt)
		})
		for i, entry := range group {
			if i < maxPerPath && now.Sub(entry.CreatedAt) <= retention {
				continue
			}
			// Data first. If this fails, keep the metadata so the entry stays
			// recognizable and the next sweep retries it.
			if err := s.removeLocked(backupName(entry.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("shadow: remove backup for entry %s: %w", entry.ID, err))
				continue
			}
			if err := s.removeLocked(metaName(entry.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("shadow: remove metadata for entry %s: %w", entry.ID, err))
			}
		}
	}

	// Pass 3: reclaim files no live entry can use.
	if err := s.sweepOrphansLocked(now, live); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	return cleanupErr
}

// sweepOrphansLocked removes store files that cannot belong to a usable entry:
// backups left behind when a metadata write never completed, metadata that no
// longer decodes, and temporary files from an interrupted metadata write.
//
// A file must be untouched for orphanGrace first, because writes from another
// Nabd process are not coordinated by this store's mutex. Metadata that cannot
// be decoded is removed here; its backup becomes orphaned and is reclaimed by
// the following sweep, so repairing takes at most two sweeps.
func (s *Store) sweepOrphansLocked(now time.Time, live map[string]struct{}) error {
	files, err := s.listStoreFilesLocked()
	if err != nil {
		return err
	}
	hasMeta := make(map[string]struct{}, len(files))
	for _, file := range files {
		if strings.HasSuffix(file.name, metaSuffix) {
			hasMeta[strings.TrimSuffix(file.name, metaSuffix)] = struct{}{}
		}
	}
	var sweepErr error
	for _, file := range files {
		orphan := false
		switch {
		case strings.HasPrefix(file.name, metaTmpPrefix):
			orphan = true
		case strings.HasSuffix(file.name, metaSuffix):
			_, ok := live[strings.TrimSuffix(file.name, metaSuffix)]
			orphan = !ok
		case strings.HasSuffix(file.name, backupExt):
			_, ok := hasMeta[strings.TrimSuffix(file.name, backupExt)]
			orphan = !ok
		}
		if !orphan {
			continue
		}
		if now.Sub(file.modified) < orphanGrace {
			continue
		}
		if err := s.removeLocked(file.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			sweepErr = errors.Join(sweepErr, fmt.Errorf("shadow: remove orphaned store file %s: %w", file.path, err))
		}
	}
	return sweepErr
}

type storeFile struct {
	// path is relative to the workspace root so it can be removed through the
	// confined root; name is the bare file name used for suffix parsing.
	path     string
	name     string
	modified time.Time
}

// listStoreFilesLocked lists regular files in the store directory. Anything
// else is reported rather than skipped so a planted entry cannot hide data.
func (s *Store) listStoreFilesLocked() ([]storeFile, error) {
	f, err := s.root.Open(baseDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("shadow: open directory: %w", err)
	}
	defer f.Close()

	var files []storeFile
	count := 0
	for {
		dirEntries, readErr := f.ReadDir(128)
		for _, dirEntry := range dirEntries {
			count++
			if count > maxStoreEntries {
				return nil, fmt.Errorf("shadow: directory scan entry limit exceeded")
			}
			if dirEntry.IsDir() {
				continue
			}
			info, infoErr := dirEntry.Info()
			if infoErr != nil {
				return nil, fmt.Errorf("shadow: inspect store entry: %w", infoErr)
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("shadow: unexpected non-regular store entry")
			}
			files = append(files, storeFile{path: baseDir + "/" + dirEntry.Name(), name: dirEntry.Name(), modified: info.ModTime()})
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("shadow: read directory: %w", readErr)
		}
		if len(dirEntries) == 0 {
			break
		}
	}
	return files, nil
}

// removeLocked deletes one store entry, going through the test seam when set.
func (s *Store) removeLocked(name string) error {
	if s.removeFile != nil {
		return s.removeFile(name)
	}
	return s.root.Remove(name)
}

func backupName(id string) string { return baseDir + "/" + id + backupExt }
func metaName(id string) string   { return baseDir + "/" + id + metaSuffix }

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
