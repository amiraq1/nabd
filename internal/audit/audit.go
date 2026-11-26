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
// Tail-truncation limitation: because there is no external out-of-band
// checkpoint, a local attacker with write access to .nabd/audit.log can delete
// trailing events (or delete the entire log) between separate runs without
// detection by a fresh Open/Verify. Within a single running Log instance, Verify
// still detects truncation below the in-memory sequence counter. A local user
// who can write both the log and NABD_AUDIT_ANCHOR can also recompute the chain.
package audit

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	dir            = ".nabd"
	fileName       = ".nabd/audit.log"
	AnchorEnv      = "NABD_AUDIT_ANCHOR"
	eventVersion1  = 1
	modeSHA256     = "sha256"
	modeHMACSHA256 = "hmac-sha256"
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
	root     *os.Root
	mu       sync.Mutex
	now      func() time.Time
	prevHash string
	sequence uint64
	mode     string
	hmacKey  []byte // derived in memory from NABD_AUDIT_ANCHOR; never persisted
}

// Open binds a log to root. Nothing is created until the first Write, so a
// read-only workspace can still be used for reading.
func Open(root *os.Root) (*Log, error) {
	if root == nil {
		return nil, errors.New("audit: nil root")
	}
	l := &Log{root: root, now: time.Now, mode: modeSHA256}
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
	lastHash, lastSeq, err := l.scanChain(true)
	if err != nil {
		return err
	}
	l.prevHash, l.sequence = lastHash, lastSeq
	return nil
}

func (l *Log) scanChain(initialLoad bool) (string, uint64, error) {
	f, err := l.root.Open(fileName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return l.genesis(), 0, nil
		}
		return "", 0, fmt.Errorf("audit: read chain: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	lastHash := l.genesis()
	var lastSeq uint64
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			if initialLoad {
				return "", 0, fmt.Errorf("audit: decode existing event: %w", err)
			}
			return "", 0, fmt.Errorf("audit: invalid event at line %d: %w", lastSeq+1, err)
		}
		lastSeq++
		if err := l.validateEventPolicy(event, lastSeq); err != nil {
			return "", 0, err
		}
		if event.Sequence != lastSeq || event.PrevHash != lastHash {
			return "", 0, fmt.Errorf("audit: broken chain at sequence %d", lastSeq)
		}
		want, err := l.computeEventHash(event)
		if err != nil || !hmac.Equal([]byte(want), []byte(event.Hash)) {
			return "", 0, fmt.Errorf("audit: hash mismatch at sequence %d", lastSeq)
		}
		lastHash = event.Hash
	}
	if err := scanner.Err(); err != nil {
		if initialLoad {
			return "", 0, fmt.Errorf("audit: scan existing chain: %w", err)
		}
		return "", 0, err
	}
	return lastHash, lastSeq, nil
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

// Write appends one event, links it to the previous hash and fsyncs it. A
// failed write does not advance the in-memory chain.
func (l *Log) Write(event Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()

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
	l.sequence = event.Sequence
	l.prevHash = event.Hash
	return nil
}

// Verify checks every sequence number, mode, and cryptographic hash/HMAC. It
// prevents silent forged appends when NABD_AUDIT_ANCHOR is held outside the
// writable log, though without an external checkpoint a fresh Verify cannot
// detect tail truncation across separate process runs.
func (l *Log) Verify() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, seq, err := l.scanChain(false)
	if err != nil {
		return err
	}
	if seq != l.sequence {
		return fmt.Errorf("audit: log has %d events, expected %d", seq, l.sequence)
	}
	return nil
}
