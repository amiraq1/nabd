// Package shadow stores pre-edit copies of files so an edit can be undone.
// The store lives under .nabd/shadow and is never exposed through the agent's
// file tools.
package shadow

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	baseDir    = ".nabd/shadow"
	metaSuffix = ".json"
	backupExt  = ".bak"
	maxPerPath = 20
	retention  = 7 * 24 * time.Hour
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
//
// Performance: after the first directory scan the full entry list is cached in
// memory. The cache is invalidated whenever a Backup or MarkUndone call
// modifies the on-disk state, so pruneLocked never re-reads JSON files it has
// already parsed.
type Store struct {
	root  *os.Root
	mu    sync.Mutex
	now   func() time.Time
	index []Entry // nil means "not yet loaded"
}

// Open creates a store bound to root. It does not write anything until the
// first Backup, so read-only workspaces can still be used for reading.
func Open(root *os.Root) (*Store, error) {
	if root == nil {
		return nil, errors.New("shadow: nil root")
	}
	return &Store{root: root, now: time.Now}, nil
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

	// Ensure index is loaded BEFORE writing new files to disk so the on-disk
	// scan does not encounter the new entry prematurely.
	_ = s.ensureIndexLocked()

	if err := s.writeNewLocked(backupName(entry.ID), original); err != nil {
		_ = s.root.Remove(backupName(entry.ID))
		return Entry{}, fmt.Errorf("shadow: write backup: %w", err)
	}
	if err := s.writeMetaLocked(entry); err != nil {
		_ = s.root.Remove(backupName(entry.ID))
		return Entry{}, err
	}

	// Update the in-memory index eagerly, then prune.
	s.index = append(s.index, entry)
	_ = s.pruneLocked(now)
	return entry, nil
}

// Latest returns the most recent usable entry, optionally restricted to one
// relative path ("" means any path), together with the original bytes.
func (s *Store) Latest(relPath string) (Entry, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureIndexLocked(); err != nil {
		return Entry{}, nil, err
	}

	var matches []Entry
	for _, entry := range s.index {
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
	data, err := s.root.ReadFile(backupName(entry.ID))
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

	_ = s.ensureIndexLocked()
	entry, err := s.readMetaLocked(id)
	if err != nil {
		return err
	}
	entry.Undone = true
	if err := s.writeMetaLocked(entry); err != nil {
		return err
	}
	// Update in-memory index to reflect the new Undone flag.
	for i := range s.index {
		if s.index[i].ID == id {
			s.index[i].Undone = true
			break
		}
	}
	return nil
}

// ensureIndexLocked loads the entry list from disk if it has not yet been
// loaded. Must be called with s.mu held. Returns nil on success.
func (s *Store) ensureIndexLocked() error {
	if s.index != nil {
		return nil
	}
	entries, err := s.loadAllLocked()
	if err != nil {
		return err
	}
	s.index = entries
	return nil
}

// loadAllLocked reads and parses every .json metadata file in baseDir.
// Must be called with s.mu held.
func (s *Store) loadAllLocked() ([]Entry, error) {
	f, err := s.root.Open(baseDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("shadow: open directory: %w", err)
	}
	defer f.Close()

	dirEntries, err := f.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("shadow: read directory: %w", err)
	}

	var entries []Entry
	for _, dirEntry := range dirEntries {
		if dirEntry.IsDir() || !strings.HasSuffix(dirEntry.Name(), metaSuffix) {
			continue
		}
		entry, err := s.readMetaLocked(strings.TrimSuffix(dirEntry.Name(), metaSuffix))
		if err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
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

// listLocked is kept for backward-compatibility with pruneLocked;
// it uses the in-memory index when available.
func (s *Store) listLocked() ([]Entry, error) {
	if s.index != nil {
		// Return a copy so callers can sort without mutating the cache.
		out := make([]Entry, len(s.index))
		copy(out, s.index)
		return out, nil
	}
	return s.loadAllLocked()
}

func (s *Store) readMetaLocked(id string) (Entry, error) {
	data, err := s.root.ReadFile(metaName(id))
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

	byPath := make(map[string][]Entry)
	for _, entry := range entries {
		byPath[entry.Path] = append(byPath[entry.Path], entry)
	}

	var pruned []string
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
			_ = s.root.Remove(backupName(entry.ID))
			_ = s.root.Remove(metaName(entry.ID))
			pruned = append(pruned, entry.ID)
		}
	}

	// Remove pruned entries from the in-memory index.
	if len(pruned) > 0 && s.index != nil {
		pruneSet := make(map[string]bool, len(pruned))
		for _, id := range pruned {
			pruneSet[id] = true
		}
		kept := s.index[:0]
		for _, e := range s.index {
			if !pruneSet[e.ID] {
				kept = append(kept, e)
			}
		}
		s.index = kept
	}
	return nil
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
