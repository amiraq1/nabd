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
	maxPerPath       = 20
	retention        = 7 * 24 * time.Hour
	maxBackupBytes   = 2 << 20
	maxMetadataBytes = 16 << 10
	maxTotalBytes    = 128 << 20
	maxStoreEntries  = 10000
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
	root            *os.Root
	mu              sync.Mutex
	now             func() time.Time
	retentionCancel context.CancelFunc
	retentionDone   chan struct{}
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
	s.StopRetention()
	runCtx, cancel := context.WithCancel(ctx)
	s.retentionCancel = cancel
	s.retentionDone = make(chan struct{})
	done := s.retentionDone
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			_ = s.Cleanup(s.now())
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// StopRetention stops the background cleanup loop.
func (s *Store) StopRetention() {
	if s == nil || s.retentionCancel == nil {
		return
	}
	s.retentionCancel()
	s.retentionCancel = nil
}

// Cleanup applies the retention quota now and reports removal failures.
func (s *Store) Cleanup(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneLocked(now)
}

// Backup saves the original bytes before an edit and records both hashes.
func (s *Store) Backup(relPath string, original []byte, mode os.FileMode, updated []byte) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.root.MkdirAll(baseDir, 0o700); err != nil {
		return Entry{}, fmt.Errorf("shadow: create directory: %w", err)
	}

	now := s.now().UTC()
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

	_ = s.pruneLocked(now)
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

	entry := matches[0]
	data, err := s.readLimited(backupName(entry.ID), maxBackupBytes)
	if err != nil {
		return Entry{}, nil, fmt.Errorf("shadow: read backup %s: %w", entry.ID, err)
	}
	if hashBytes(data) != entry.SHA256Before {
		return Entry{}, nil, fmt.Errorf("shadow: backup %s is corrupted", entry.ID)
	}
	return entry, data, nil
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
	tmp := baseDir + "/.tmp-" + randomHex(8)
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

func (s *Store) pruneLocked(now time.Time) error {
	entries, err := s.listLocked()
	if err != nil {
		return err
	}
	var cleanupErr error

	byPath := make(map[string][]Entry)
	for _, entry := range entries {
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
			// Remove metadata first so a later Latest never sees an entry
			// whose backup has already been deleted.
			if err := s.root.Remove(metaName(entry.ID)); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
			}
			if err := s.root.Remove(backupName(entry.ID)); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
			}
		}
	}
	return cleanupErr
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
