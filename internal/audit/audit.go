// Package audit appends a hash-only record of tool activity to .nabd/audit.log.
// Applied events also use a durable pending journal so a transient log-write
// failure cannot silently discard the operation's final audit event.
package audit

import (
	"bufio"
	"crypto/rand"
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
	dir        = ".nabd"
	fileName   = ".nabd/audit.log"
	pendingDir = ".nabd/audit-pending"
)

// Event is one JSON line in the audit log. ID is populated for applied events
// and makes replay from the pending journal idempotent.
type Event struct {
	ID           string    `json:"id,omitempty"`
	Time         time.Time `json:"time"`
	Event        string    `json:"event"`
	Tool         string    `json:"tool,omitempty"`
	ArgsSHA256   string    `json:"args_sha256,omitempty"`
	ResultSHA256 string    `json:"result_sha256,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	Reason       string    `json:"reason,omitempty"`
}

// Log appends events inside an already-jailed workspace root.
type Log struct {
	root *os.Root
	mu   sync.Mutex
	now  func() time.Time
}

// Open binds a log to root and replays any durable applied events left pending
// by a previous log-write failure. A read-only workspace remains usable when no
// pending journal exists.
func Open(root *os.Root) (*Log, error) {
	if root == nil {
		return nil, errors.New("audit: nil root")
	}
	l := &Log{root: root, now: time.Now}
	if err := l.RecoverPending(); err != nil {
		return nil, err
	}
	return l, nil
}

// Write appends one ordinary event and fsyncs it.
func (l *Log) Write(event Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendEvent(l.withTime(event))
}

// RecordApplied first persists the final event in a side journal, then appends
// it to audit.log and removes the journal entry. If audit.log cannot be written,
// the event remains durable and will be replayed by RecoverPending or Open.
func (l *Log) RecordApplied(event Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	event = l.withTime(event)
	if event.Event != "applied" {
		return errors.New("audit: RecordApplied requires an applied event")
	}
	if event.ID == "" {
		id, err := newID()
		if err != nil {
			return fmt.Errorf("audit: create applied event id: %w", err)
		}
		event.ID = id
	}
	if err := l.writePending(event); err != nil {
		if appendErr := l.appendUnique(event); appendErr == nil {
			return nil
		} else {
			return fmt.Errorf("audit: operation applied but journal and log writes failed: %w", errors.Join(err, appendErr))
		}
	}
	if err := l.appendUnique(event); err != nil {
		return fmt.Errorf("audit: applied event is durable in pending journal: %w", err)
	}
	if err := l.root.Remove(pendingDir + "/" + event.ID + ".json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("audit: applied event is logged; pending journal cleanup failed: %w", err)
	}
	if err := syncDirectory(l.root, pendingDir); err != nil {
		return fmt.Errorf("audit: applied event is logged; pending journal directory sync failed: %w", err)
	}
	return nil
}

// RecoverPending replays journaled applied events. Event IDs prevent duplicates
// if the process crashed after appending audit.log but before deleting the
// corresponding pending file.
func (l *Log) RecoverPending() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	dirFile, err := l.root.Open(pendingDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("audit: open pending journal: %w", err)
	}
	entries, readErr := dirFile.ReadDir(-1)
	closeErr := dirFile.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return fmt.Errorf("audit: read pending journal: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := pendingDir + "/" + entry.Name()
		f, err := l.root.Open(path)
		if err != nil {
			return fmt.Errorf("audit: open pending event %s: %w", entry.Name(), err)
		}
		data, readErr := io.ReadAll(f)
		closeErr := f.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return fmt.Errorf("audit: read pending event %s: %w", entry.Name(), err)
		}
		var event Event
		if err := json.Unmarshal(data, &event); err != nil || event.Event != "applied" || event.ID == "" {
			return fmt.Errorf("audit: invalid pending event %s", entry.Name())
		}
		if err := l.appendUnique(event); err != nil {
			return fmt.Errorf("audit: replay pending event %s: %w", entry.Name(), err)
		}
		if err := l.root.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("audit: remove replayed event %s: %w", entry.Name(), err)
		}
		if err := syncDirectory(l.root, pendingDir); err != nil {
			return fmt.Errorf("audit: sync replayed event removal %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (l *Log) withTime(event Event) Event {
	if event.Time.IsZero() {
		event.Time = l.now().UTC()
	}
	return event
}

func newID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func (l *Log) writePending(event Event) error {
	if err := l.root.MkdirAll(pendingDir, 0o700); err != nil {
		return fmt.Errorf("create pending directory: %w", err)
	}
	for _, path := range []string{".", dir, pendingDir} {
		if err := syncDirectory(l.root, path); err != nil {
			return fmt.Errorf("sync pending directory %s: %w", path, err)
		}
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode pending event: %w", err)
	}
	path := pendingDir + "/" + event.ID + ".json"
	f, err := l.root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create pending event: %w", err)
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = l.root.Remove(path)
		return fmt.Errorf("write pending event: %w", err)
	}
	if err := syncDirectory(l.root, pendingDir); err != nil {
		return fmt.Errorf("sync pending journal entry: %w", err)
	}
	return nil
}

func (l *Log) appendUnique(event Event) error {
	if event.ID != "" {
		exists, err := l.containsID(event.ID)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
	}
	return l.appendEvent(event)
}

func (l *Log) containsID(id string) (bool, error) {
	f, err := l.root.Open(fileName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("open audit log for replay check: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return false, fmt.Errorf("decode existing audit log line: %w", err)
		}
		if event.ID == id {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("scan audit log: %w", err)
	}
	return false, nil
}

func (l *Log) appendEvent(event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("audit: encode event: %w", err)
	}
	data = append(data, '\n')
	if err := l.root.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("audit: create directory: %w", err)
	}
	f, err := l.root.OpenFile(fileName, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("audit: open log: %w", err)
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("audit: append event: %w", err)
	}
	return nil
}
