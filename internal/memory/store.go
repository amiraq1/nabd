package memory

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Store persists workspace-scoped memory entries in .nabd/memory/entries.jsonl
// under cross-process locking via .nabd/memory/memory.lock.
type Store struct {
	root         *os.Root
	now          func() time.Time
	lockWait     time.Duration
	randRead     func([]byte) (int, error)
	syncDir      func(*os.File) error
	beforeRename func()
	writeHook    func(*os.File, []byte) error
}

// Open binds a memory Store to a trusted workspace root without creating any
// directory or file on disk.
func Open(root *os.Root) (*Store, error) {
	if root == nil {
		return nil, errors.New("memory: nil root")
	}
	return &Store{
		root:     root,
		now:      time.Now,
		lockWait: DefaultLockWait,
		randRead: rand.Read,
		syncDir:  syncDirectoryFD,
	}, nil
}

// DirectorySyncSupported reports whether the current OS supports durable parent
// directory fsync after file replacement.
func (s *Store) DirectorySyncSupported() bool {
	return directorySyncSupported()
}

// Save validates, secret-checks, assigns a collision-checked ID, and atomically
// persists a new memory entry under .nabd/memory/memory.lock.
func (s *Store) Save(ctx context.Context, input SaveInput) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	canon, _, err := CanonicalizeSaveInput(input)
	if err != nil {
		return Entry{}, err
	}

	h, err := openMemoryDir(s.root, true)
	if err != nil {
		return Entry{}, err
	}
	defer h.close()

	lock, err := acquireMemoryLock(ctx, h, s.effectiveLockWait())
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = lock.close() }()

	entries, err := s.readValidatedEntriesLocked(h)
	if err != nil {
		return Entry{}, err
	}
	if len(entries) >= MaxEntries {
		return Entry{}, fmt.Errorf("%w: active entry limit of %d reached", ErrLimitExceeded, MaxEntries)
	}

	existingIDs := make(map[string]struct{}, len(entries))
	for _, existing := range entries {
		existingIDs[existing.ID] = struct{}{}
	}

	id, err := s.generateUniqueID(existingIDs)
	if err != nil {
		return Entry{}, err
	}

	entry := Entry{
		ID:        id,
		Kind:      Kind(canon.Kind),
		Title:     canon.Title,
		Body:      canon.Body,
		Tags:      append([]string(nil), canon.Tags...),
		CreatedAt: s.now().UTC().Format(time.RFC3339),
	}

	updated := append(entries, entry)
	payload, err := encodeEntriesJSONL(updated)
	if err != nil {
		return Entry{}, err
	}
	if err := writeEntriesAtomic(h, payload, s.syncDir, s.beforeRename, s.writeHook); err != nil {
		var uncertain *CommitUncertainError
		if errors.As(err, &uncertain) {
			return entry, err
		}
		return Entry{}, err
	}
	return entry, nil
}

// Search ranks active entries matching query (and optional kind) via Okapi BM25
// under the cross-process lock, returning at most MaxSearchOutputBytes of JSON.
func (s *Store) Search(ctx context.Context, query, kind string, maxResults *int) (SearchResponse, []byte, error) {
	if err := ctx.Err(); err != nil {
		return SearchResponse{}, nil, err
	}
	normQuery, normKind, limit, err := ValidateSearchInput(query, kind, maxResults)
	if err != nil {
		return SearchResponse{}, nil, err
	}

	entries, err := s.loadEntriesForRead(ctx)
	if err != nil {
		return SearchResponse{}, nil, err
	}

	matches, total := SearchEntries(entries, normQuery, normKind, limit)
	return MarshalSearchResponse(total, matches)
}

// List returns active entries sorted newest-first (created_at desc, id asc),
// returning up to limit entries (all if limit <= 0) and the total active count.
func (s *Store) List(ctx context.Context, limit int) ([]Entry, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	entries, err := s.loadEntriesForRead(ctx)
	if err != nil {
		return nil, 0, err
	}
	total := len(entries)
	sort.Slice(entries, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, entries[i].CreatedAt)
		tj, _ := time.Parse(time.RFC3339, entries[j].CreatedAt)
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return entries[i].ID < entries[j].ID
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, total, nil
}

// Get retrieves a single entry by ID under the cross-process lock.
func (s *Store) Get(ctx context.Context, id string) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	if err := ValidateID(id); err != nil {
		return Entry{}, err
	}
	entries, err := s.loadEntriesForRead(ctx)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range entries {
		if e.ID == id {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("%w: %s", ErrNotFound, id)
}

// Delete removes the entry identified by id under the cross-process lock and
// atomically rewrites entries.jsonl so capacity is immediately reclaimed.
func (s *Store) Delete(ctx context.Context, id string) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	if err := ValidateID(id); err != nil {
		return Entry{}, err
	}

	h, err := openMemoryDir(s.root, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Entry{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return Entry{}, err
	}
	defer h.close()

	lock, err := acquireMemoryLock(ctx, h, s.effectiveLockWait())
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = lock.close() }()

	entries, err := s.readValidatedEntriesLocked(h)
	if err != nil {
		return Entry{}, err
	}

	idx := -1
	var removed Entry
	for i, e := range entries {
		if e.ID == id {
			idx = i
			removed = e
			break
		}
	}
	if idx < 0 {
		return Entry{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}

	remaining := make([]Entry, 0, len(entries)-1)
	remaining = append(remaining, entries[:idx]...)
	remaining = append(remaining, entries[idx+1:]...)

	payload, err := encodeEntriesJSONL(remaining)
	if err != nil {
		return Entry{}, err
	}
	if err := writeEntriesAtomic(h, payload, s.syncDir, s.beforeRename, s.writeHook); err != nil {
		var uncertain *CommitUncertainError
		if errors.As(err, &uncertain) {
			return removed, err
		}
		return Entry{}, err
	}
	return removed, nil
}

func (s *Store) effectiveLockWait() time.Duration {
	if s.lockWait > 0 {
		return s.lockWait
	}
	return DefaultLockWait
}

func (s *Store) generateUniqueID(existing map[string]struct{}) (string, error) {
	randFn := s.randRead
	if randFn == nil {
		randFn = rand.Read
	}
	var buf [4]byte
	for attempt := 0; attempt < maxIDRetries; attempt++ {
		if _, err := randFn(buf[:]); err != nil {
			return "", fmt.Errorf("memory: generate entry id: %w", err)
		}
		candidate := "mem_" + hex.EncodeToString(buf[:])
		if _, dup := existing[candidate]; !dup {
			return candidate, nil
		}
	}
	return "", errors.New("memory: exhausted id generation attempts due to collision")
}

func (s *Store) loadEntriesForRead(ctx context.Context) ([]Entry, error) {
	h, err := openMemoryDir(s.root, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer h.close()

	lock, err := acquireMemoryLock(ctx, h, s.effectiveLockWait())
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.close() }()

	return s.readValidatedEntriesLocked(h)
}

func (s *Store) readValidatedEntriesLocked(h *memoryDirHandle) ([]Entry, error) {
	data, err := readEntriesFile(h, MaxFileBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return parseEntriesJSONL(data)
}

func parseEntriesJSONL(data []byte) ([]Entry, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if int64(len(data)) > MaxFileBytes {
		return nil, fmt.Errorf("%w: file exceeds %d bytes", ErrLimitExceeded, MaxFileBytes)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("%w: non-UTF-8 or NUL bytes in entries file", ErrCorruptStore)
	}
	if data[len(data)-1] != '\n' {
		return nil, fmt.Errorf("%w: incomplete trailing line (missing final newline)", ErrCorruptStore)
	}

	rawLines := bytes.Split(data[:len(data)-1], []byte("\n"))
	if len(rawLines) > MaxEntries {
		return nil, fmt.Errorf("%w: entry count %d exceeds limit %d", ErrLimitExceeded, len(rawLines), MaxEntries)
	}

	entries := make([]Entry, 0, len(rawLines))
	seenIDs := make(map[string]struct{}, len(rawLines))
	for i, rawLine := range rawLines {
		if len(bytes.TrimSpace(rawLine)) == 0 {
			return nil, fmt.Errorf("%w: unexpected blank line at record %d", ErrCorruptStore, i+1)
		}
		var e Entry
		if err := decodeStrictStoredEntry(rawLine, &e); err != nil {
			return nil, fmt.Errorf("%w: record %d: %v", ErrCorruptStore, i+1, err)
		}
		if err := validateStoredEntry(e); err != nil {
			return nil, fmt.Errorf("record %d: %w", i+1, err)
		}
		if _, dup := seenIDs[e.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate entry id %s", ErrCorruptStore, e.ID)
		}
		seenIDs[e.ID] = struct{}{}
		entries = append(entries, e)
	}
	return entries, nil
}

func decodeStrictStoredEntry(raw []byte, dst *Entry) error {
	if err := validateNoDuplicateJSONKeys(raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return errors.New("unexpected trailing JSON data")
	}
	return nil
}

func encodeEntriesJSONL(entries []Entry) ([]byte, error) {
	if len(entries) > MaxEntries {
		return nil, fmt.Errorf("%w: entry count %d exceeds limit %d", ErrLimitExceeded, len(entries), MaxEntries)
	}
	var b bytes.Buffer
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			return nil, fmt.Errorf("memory: encode entry %s: %w", e.ID, err)
		}
		b.Write(line)
		b.WriteByte('\n')
		if b.Len() > MaxFileBytes {
			return nil, fmt.Errorf("%w: serialized file would exceed %d bytes", ErrLimitExceeded, MaxFileBytes)
		}
	}
	return b.Bytes(), nil
}

// Helper for tests needing direct inspection of memory directory path.
func isWhitespaceOnly(s string) bool {
	return strings.TrimSpace(s) == ""
}
