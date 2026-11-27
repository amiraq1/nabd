// Package audit appends a hash-chained, hash-only record of tool activity to
// .nabd/audit.log. File contents and arguments are never written; only hashes
// and short sanitized workspace-relative details are stored.
//
// When NABD_AUDIT_ANCHOR is set, events are authenticated with HMAC-SHA256 using
// a key derived in memory from NABD_AUDIT_ANCHOR; neither the anchor nor its
// derived key is ever written to the log. When NABD_AUDIT_ANCHOR is unset, the
// chain uses unkeyed SHA-256 (`mode: "sha256"`) for integrity against non-hostile
// corruption only.
//
// Appends are serialized across processes with .nabd/audit.lock. Under that lock
// the writer re-checks the log's file identity, size, modification time and last
// record before extending the chain, so a cooperating second process cannot
// duplicate a sequence number. The lock only coordinates writers that honor it:
// a process with write access can still edit the file outside it.
//
// Tail-truncation limitation: because there is no external out-of-band
// checkpoint, a local attacker with write access to .nabd/audit.log can delete
// trailing events (or delete the entire log) between separate runs without
// detection by a fresh Open/Verify. Within a single running Log instance, Verify
// still detects truncation below the in-memory sequence counter. A same-length
// rewrite of an earlier prefix is only detected by a full Open/Verify scan or an
// independent checkpoint; the incremental append path does not re-read the whole
// file. A local user who can write both the log and NABD_AUDIT_ANCHOR can also
// recompute the chain.
package audit

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	dir            = ".nabd"
	fileName       = ".nabd/audit.log"
	lockName       = "audit.lock"
	AnchorEnv      = "NABD_AUDIT_ANCHOR"
	eventVersion1  = 1
	modeSHA256     = "sha256"
	modeHMACSHA256 = "hmac-sha256"

	defaultLockWait    = 10 * time.Second
	lockRetryInterval  = 10 * time.Millisecond
	maxAppendVerifyLen = 32 << 20
)

// Event is one JSON line in the audit log.
type Event struct {
	Version      int       `json:"v,omitempty"`
	Mode         string    `json:"mode,omitempty"`
	Time         time.Time `json:"time"`
	Event        string    `json:"event"`
	Tool         string    `json:"tool,omitempty"`
	ArgsSHA256   string    `json:"args_sha256,omitempty"`
	ResultSHA256 string    `json:"result_sha256,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	Sequence     uint64    `json:"seq"`
	PrevHash     string    `json:"prev_hash"`
	Hash         string    `json:"hash"`
}

type hashInput struct {
	Version      int       `json:"v,omitempty"`
	Mode         string    `json:"mode,omitempty"`
	Time         time.Time `json:"time"`
	Event        string    `json:"event"`
	Tool         string    `json:"tool,omitempty"`
	ArgsSHA256   string    `json:"args_sha256,omitempty"`
	ResultSHA256 string    `json:"result_sha256,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	Sequence     uint64    `json:"seq"`
	PrevHash     string    `json:"prev_hash"`
}

// Log appends events inside an already-jailed workspace root.
type Log struct {
	root *os.Root
	mu   sync.Mutex
	now  func() time.Time

	prevHash string
	sequence uint64
	mode     string
	hmacKey  []byte // derived in memory from NABD_AUDIT_ANCHOR; never persisted

	anchor   chainAnchor
	anchored bool
	lockWait time.Duration

	// syncFile and syncDir are test seams. Production uses f.Sync and the
	// platform directory sync; neither is a package-level hook, so tests never
	// share mutable state with concurrent tests.
	syncFile func(*os.File) error
	syncDir  func(*os.File) error
}

// chainAnchor records the verified end of the chain, so an append can detect a
// replaced, truncated or rewritten tail without re-reading the whole file.
type chainAnchor struct {
	offset  int64
	tail    []byte
	modTime time.Time
	id      fileID
}

type chainState struct {
	lastHash string
	seq      uint64
	size     int64
	tail     []byte
}

// Open binds a log to root. It does not create the log (or its lock) until the
// first Write, so a workspace without an existing log can still be read.
func Open(root *os.Root) (*Log, error) {
	if root == nil {
		return nil, errors.New("audit: nil root")
	}
	l := &Log{
		root:     root,
		now:      time.Now,
		mode:     modeSHA256,
		lockWait: defaultLockWait,
		syncFile: func(f *os.File) error { return f.Sync() },
		syncDir:  syncDirectory,
	}
	if value := os.Getenv(AnchorEnv); value != "" {
		sum := sha256.Sum256([]byte(value))
		key := make([]byte, len(sum))
		copy(key, sum[:])
		l.hmacKey = key
		l.mode = modeHMACSHA256
	}
	if err := l.loadChain(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Log) loadChain() error {
	if _, err := l.root.Lstat(fileName); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			l.prevHash, l.sequence = l.genesis(), 0
			l.anchored = false
			return nil
		}
		return fmt.Errorf("audit: inspect log: %w", err)
	}

	lock, err := acquireAuditLock(l.root, l.lockWait)
	if err != nil {
		return err
	}
	defer lock.close()

	f, err := l.root.Open(fileName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// The log disappeared between Lstat and the locked open. Start from
			// genesis; no chain was verified, so nothing is trusted yet.
			l.prevHash, l.sequence = l.genesis(), 0
			l.anchored = false
			return nil
		}
		return fmt.Errorf("audit: read chain: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("audit: stat chain: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("audit: log is not a regular file")
	}
	id, err := fileIdentity(f)
	if err != nil {
		return fmt.Errorf("audit: identify log: %w", err)
	}
	state, err := l.scanFrom(f, 0, l.genesis(), 0, false)
	if err != nil {
		return err
	}
	l.prevHash, l.sequence = state.lastHash, state.seq
	l.anchor = chainAnchor{offset: state.size, tail: state.tail, modTime: info.ModTime(), id: id}
	l.anchored = true
	return nil
}

// scanFrom verifies a section of the log. start must be a record boundary;
// baseHash/baseSeq identify the verified state immediately before start.
// requireFinalNewline rejects a partially written trailing record, which is
// only acceptable while loading an existing file.
func (l *Log) scanFrom(f *os.File, start int64, baseHash string, baseSeq uint64, requireFinalNewline bool) (chainState, error) {
	state := chainState{lastHash: baseHash, seq: baseSeq, size: start}
	if start < 0 {
		return state, errors.New("audit: invalid scan offset")
	}
	info, err := f.Stat()
	if err != nil {
		return state, fmt.Errorf("audit: stat chain: %w", err)
	}
	end := info.Size()
	if start > end {
		return state, fmt.Errorf("audit: log is shorter than the verified chain (%d < %d bytes)", end, start)
	}
	if start == end {
		return state, nil
	}

	reader := bufio.NewReaderSize(io.NewSectionReader(f, start, end-start), 64<<10)
	pos := start
	for {
		raw, readErr := reader.ReadBytes('\n')
		if len(raw) > 0 {
			complete := raw[len(raw)-1] == '\n'
			line := raw
			if complete {
				line = raw[:len(raw)-1]
			}
			if !complete && requireFinalNewline {
				return chainState{}, fmt.Errorf("audit: incomplete trailing record at offset %d", pos)
			}
			trimmed := strings.TrimSpace(string(line))
			if trimmed != "" {
				var event Event
				if err := json.Unmarshal([]byte(trimmed), &event); err != nil {
					return chainState{}, fmt.Errorf("audit: invalid event at offset %d: %w", pos, err)
				}
				state.seq++
				if err := l.validateEventPolicy(event, state.seq); err != nil {
					return chainState{}, err
				}
				if event.Sequence != state.seq || event.PrevHash != state.lastHash {
					return chainState{}, fmt.Errorf("audit: broken chain at sequence %d", state.seq)
				}
				want, err := l.computeEventHash(event)
				if err != nil || !hmac.Equal([]byte(want), []byte(event.Hash)) {
					return chainState{}, fmt.Errorf("audit: hash mismatch at sequence %d", state.seq)
				}
				state.lastHash = event.Hash
				state.tail = append([]byte(nil), raw...)
			}
			pos += int64(len(raw))
			state.size = pos
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return chainState{}, fmt.Errorf("audit: scan chain: %w", readErr)
		}
	}
	return state, nil
}

func (l *Log) validateEventPolicy(event Event, seq uint64) error {
	if l.mode == modeHMACSHA256 {
		if event.Version == 0 {
			return fmt.Errorf("audit: existing log uses legacy v0 format at sequence %d while %s is set; archive or rotate %s manually before continuing", seq, AnchorEnv, fileName)
		}
		if event.Version != eventVersion1 {
			return fmt.Errorf("audit: unsupported event version %d at sequence %d", event.Version, seq)
		}
		if event.Mode != modeHMACSHA256 {
			return fmt.Errorf("audit: existing log uses unauthenticated mode %q at sequence %d while %s is set; archive or rotate %s manually rather than downgrading", event.Mode, seq, AnchorEnv, fileName)
		}
		return nil
	}

	// Unanchored mode (NABD_AUDIT_ANCHOR unset)
	if event.Version == eventVersion1 {
		if event.Mode == modeHMACSHA256 {
			return fmt.Errorf("audit: existing log requires HMAC verification (%s) at sequence %d, but %s is unset; set %s or archive/rotate %s manually", modeHMACSHA256, seq, AnchorEnv, AnchorEnv, fileName)
		}
		if event.Mode != modeSHA256 {
			return fmt.Errorf("audit: unsupported event mode %q at sequence %d", event.Mode, seq)
		}
		return nil
	}
	if event.Version == 0 {
		if event.Mode != "" {
			return fmt.Errorf("audit: unexpected mode %q on legacy v0 event at sequence %d", event.Mode, seq)
		}
		return nil
	}
	return fmt.Errorf("audit: unsupported event version %d at sequence %d", event.Version, seq)
}

func (l *Log) genesis() string {
	return strings.Repeat("0", 64)
}

func (l *Log) computeEventHash(event Event) (string, error) {
	input := hashInput{
		Version: event.Version, Mode: event.Mode,
		Time: event.Time, Event: event.Event, Tool: event.Tool,
		ArgsSHA256: event.ArgsSHA256, ResultSHA256: event.ResultSHA256,
		Detail: event.Detail, Reason: event.Reason,
		Sequence: event.Sequence, PrevHash: event.PrevHash,
	}
	data, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	payload := append([]byte(event.PrevHash+"|"), data...)
	if event.Mode == modeHMACSHA256 || l.mode == modeHMACSHA256 {
		if len(l.hmacKey) == 0 {
			return "", errors.New("audit: missing HMAC key for anchored verification")
		}
		mac := hmac.New(sha256.New, l.hmacKey)
		mac.Write(payload)
		return hex.EncodeToString(mac.Sum(nil)), nil
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// Write appends one event, links it to the previous hash, and fsyncs it while
// holding .nabd/audit.lock. The chain is re-verified under that lock, so a
// concurrent writer cannot reuse a sequence number. A failed write does not
// advance the in-memory chain.
func (l *Log) Write(event Event) (err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	lock, lockErr := acquireAuditLock(l.root, l.lockWait)
	if lockErr != nil {
		return lockErr
	}
	defer func() {
		if closeErr := lock.close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("audit: release lock: %w", closeErr))
		}
	}()

	existed := true
	if _, statErr := l.root.Lstat(fileName); statErr != nil {
		if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("audit: inspect log: %w", statErr)
		}
		existed = false
	}
	if err := l.root.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("audit: create directory: %w", err)
	}
	f, err := l.root.OpenFile(fileName, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("audit: open log: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("audit: close log: %w", closeErr))
		}
	}()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("audit: stat log: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("audit: log is not a regular file")
	}
	id, err := fileIdentity(f)
	if err != nil {
		return fmt.Errorf("audit: identify log: %w", err)
	}
	if err := l.verifyAppendPoint(f, info, id); err != nil {
		return err
	}

	if event.Time.IsZero() {
		event.Time = l.now().UTC()
	}
	event.Version = eventVersion1
	event.Mode = l.mode
	event.Sequence = l.sequence + 1
	event.PrevHash = l.prevHash
	hash, err := l.computeEventHash(event)
	if err != nil {
		return fmt.Errorf("audit: hash event: %w", err)
	}
	event.Hash = hash
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("audit: encode event: %w", err)
	}
	data = append(data, '\n')
	line := append([]byte(nil), data...)
	expectedSize := l.anchor.offset + int64(len(data))

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("audit: append event: %w", err)
	}
	if err := l.syncFile(f); err != nil {
		return fmt.Errorf("audit: sync log: %w", err)
	}
	written, err := f.Stat()
	if err != nil {
		return fmt.Errorf("audit: stat appended log: %w", err)
	}
	if written.Size() != expectedSize {
		return fmt.Errorf("audit: append size mismatch (%d bytes, expected %d)", written.Size(), expectedSize)
	}

	// The record is on disk and synced. Advance the in-memory chain before any
	// directory sync so a retry cannot append the same event twice.
	l.sequence = event.Sequence
	l.prevHash = event.Hash
	l.anchor = chainAnchor{offset: written.Size(), tail: line, modTime: written.ModTime(), id: id}
	l.anchored = true

	if !existed {
		if syncErr := l.syncLogDirectory(); syncErr != nil {
			return syncErr
		}
	}
	return nil
}

// verifyAppendPoint revalidates the log under the cross-process lock, verifies
// any records another writer appended since the last known offset, and refreshes
// the in-memory chain head.
func (l *Log) verifyAppendPoint(f *os.File, info os.FileInfo, id fileID) error {
	size := info.Size()

	if l.anchored && id != l.anchor.id {
		return errors.New("audit: log file identity changed since it was opened (possible replacement); refusing to append")
	}
	if size < l.anchor.offset {
		return fmt.Errorf("audit: log shrank from %d to %d bytes; refusing to append", l.anchor.offset, size)
	}

	if !l.anchored {
		if size > 0 {
			state, err := l.scanFrom(f, 0, l.genesis(), 0, true)
			if err != nil {
				return err
			}
			l.prevHash, l.sequence = state.lastHash, state.seq
			l.anchor = chainAnchor{offset: state.size, tail: state.tail, modTime: info.ModTime(), id: id}
		}
		l.anchor.id = id
		l.anchor.modTime = info.ModTime()
		l.anchored = true
		return nil
	}

	switch {
	case size > l.anchor.offset:
		if size-l.anchor.offset > maxAppendVerifyLen {
			return fmt.Errorf("audit: %d unverified bytes appended; refusing to append", size-l.anchor.offset)
		}
		state, err := l.scanFrom(f, l.anchor.offset, l.prevHash, l.sequence, true)
		if err != nil {
			return err
		}
		l.prevHash, l.sequence = state.lastHash, state.seq
		l.anchor.offset = state.size
		l.anchor.tail = state.tail
		l.anchor.modTime = info.ModTime()
	case size == l.anchor.offset:
		if !info.ModTime().Equal(l.anchor.modTime) {
			// Metadata changed without a size change: a prefix may have been
			// rewritten. Re-verify the whole file rather than trusting the tail.
			state, err := l.scanFrom(f, 0, l.genesis(), 0, false)
			if err != nil {
				return err
			}
			if state.size != size {
				return errors.New("audit: log changed while it was being verified")
			}
			l.prevHash, l.sequence = state.lastHash, state.seq
			l.anchor.tail = state.tail
			l.anchor.modTime = info.ModTime()
			break
		}
		if len(l.anchor.tail) > 0 {
			tailStart := l.anchor.offset - int64(len(l.anchor.tail))
			if tailStart < 0 {
				return errors.New("audit: invalid verified tail anchor")
			}
			buf := make([]byte, len(l.anchor.tail))
			if _, err := f.ReadAt(buf, tailStart); err != nil {
				return fmt.Errorf("audit: re-read last record: %w", err)
			}
			if !bytes.Equal(buf, l.anchor.tail) {
				// A trailing blank line or a rewritten tail invalidates the fast
				// path; re-verify the whole file before deciding to fail.
				state, err := l.scanFrom(f, 0, l.genesis(), 0, false)
				if err != nil {
					return err
				}
				if state.size != size {
					return errors.New("audit: log changed while it was being verified")
				}
				l.prevHash, l.sequence = state.lastHash, state.seq
				l.anchor.tail = state.tail
				l.anchor.modTime = info.ModTime()
			}
		}
	}
	l.anchor.id = id
	return nil
}

func (l *Log) syncLogDirectory() error {
	d, err := l.root.Open(dir)
	if err != nil {
		return fmt.Errorf("audit: open .nabd for sync: %w", err)
	}
	defer d.Close()
	if err := l.syncDir(d); err != nil {
		return fmt.Errorf("audit: sync .nabd directory: %w", err)
	}
	return nil
}

// Verify checks every sequence number, mode, and cryptographic hash/HMAC. It
// prevents silent forged appends when NABD_AUDIT_ANCHOR is held outside the
// writable log, though without an external checkpoint a fresh Verify cannot
// detect tail truncation across separate process runs.
func (l *Log) Verify() (err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	lock, lockErr := acquireAuditLock(l.root, l.lockWait)
	if lockErr != nil {
		return lockErr
	}
	defer func() {
		if closeErr := lock.close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("audit: release lock: %w", closeErr))
		}
	}()

	f, openErr := l.root.Open(fileName)
	if openErr != nil {
		if errors.Is(openErr, os.ErrNotExist) {
			if l.sequence != 0 {
				return fmt.Errorf("audit: log has 0 events, expected %d", l.sequence)
			}
			return nil
		}
		return fmt.Errorf("audit: read chain: %w", openErr)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("audit: stat chain: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("audit: log is not a regular file")
	}
	id, err := fileIdentity(f)
	if err != nil {
		return fmt.Errorf("audit: identify log: %w", err)
	}
	if l.anchored && id != l.anchor.id {
		return errors.New("audit: log file identity changed since it was opened")
	}

	state, err := l.scanFrom(f, 0, l.genesis(), 0, false)
	if err != nil {
		return err
	}
	// A cooperating process may have appended more events since this Log last
	// wrote. That extends the verified chain and is legitimate; only a shorter
	// log means events this instance saw are missing.
	if state.seq < l.sequence {
		return fmt.Errorf("audit: log has %d events, expected at least %d", state.seq, l.sequence)
	}
	l.prevHash, l.sequence = state.lastHash, state.seq
	l.anchor = chainAnchor{offset: state.size, tail: state.tail, modTime: info.ModTime(), id: id}
	l.anchored = true
	return nil
}
