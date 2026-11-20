// Package audit appends an immutable, hash-only record of tool activity to
// .nabd/audit.log. File contents and arguments are never written; only hashes
// and short workspace-relative details are stored.
package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

const (
	dir      = ".nabd"
	fileName = ".nabd/audit.log"
)

// Event is one JSON line in the audit log.
type Event struct {
	Time         time.Time `json:"time"`
	Event        string    `json:"event"`
	Tool         string    `json:"tool,omitempty"`
	ArgsSHA256   string    `json:"args_sha256,omitempty"`
	ResultSHA256 string    `json:"result_sha256,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	Reason       string    `json:"reason,omitempty"`
}

// Log appends events inside an already-jailed workspace root.
// The underlying file handle is kept open after the first write to avoid
// repeated open/close syscalls on every event.
type Log struct {
	root *os.Root
	mu   sync.Mutex
	now  func() time.Time
	f    *os.File // nil until first write
}

// Open binds a log to root. Nothing is created until the first Write, so a
// read-only workspace can still be used for reading.
func Open(root *os.Root) (*Log, error) {
	if root == nil {
		return nil, errors.New("audit: nil root")
	}
	return &Log{root: root, now: time.Now}, nil
}

// Close flushes and releases the persistent file handle, if one is open.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		err := l.f.Close()
		l.f = nil
		return err
	}
	return nil
}

// openLocked ensures the log directory and file are open. Must be called with
// l.mu held.
func (l *Log) openLocked() error {
	if l.f != nil {
		return nil
	}
	if err := l.root.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("audit: create directory: %w", err)
	}
	f, err := l.root.OpenFile(fileName, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("audit: open log: %w", err)
	}
	l.f = f
	return nil
}

// Write appends one event and fsyncs it. fsync is mandatory for every call so
// that security-critical events (approval, denial, applied, failed) are
// durable before the caller proceeds.
func (l *Log) Write(event Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.writeLocked(event, true)
}

// WriteBestEffort appends one event without calling fsync. Use this only for
// ReadOnly tool events (e.g. "requested" / "applied" on read_file, list_dir,
// search_files) where durability is desirable but not required for safety.
// All security-critical events (edit approvals, denials, go_verify, mcp_call)
// must use Write instead.
func (l *Log) WriteBestEffort(event Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.writeLocked(event, false)
}

func (l *Log) writeLocked(event Event, requireSync bool) error {
	if event.Time.IsZero() {
		event.Time = l.now().UTC()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("audit: encode event: %w", err)
	}
	data = append(data, '\n')

	if err := l.openLocked(); err != nil {
		return err
	}

	_, writeErr := l.f.Write(data)
	var syncErr error
	if requireSync {
		syncErr = l.f.Sync()
	}
	if err := errors.Join(writeErr, syncErr); err != nil {
		// Close the handle so the next Write re-opens it cleanly.
		_ = l.f.Close()
		l.f = nil
		return fmt.Errorf("audit: append event: %w", err)
	}
	return nil
}
