// Package audit appends a hash-chained, hash-only record of tool activity to
// .nabd/audit.log. File contents and arguments are never written; only hashes
// and short sanitized workspace-relative details are stored.
//
// The chain is tamper-evident only when the verifier knows a trusted anchor
// outside the log (NABD_AUDIT_ANCHOR). A local user who can write both the log
// and that anchor can recompute the chain; the chain is not a guarantee
// against a local administrator.
package audit

import (
	"bufio"
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
	dir       = ".nabd"
	fileName  = ".nabd/audit.log"
	AnchorEnv = "NABD_AUDIT_ANCHOR"
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
	Sequence     uint64    `json:"seq"`
	PrevHash     string    `json:"prev_hash"`
	Hash         string    `json:"hash"`
}

type hashInput struct {
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
	anchor   string
}

// Open binds a log to root. Nothing is created until the first Write, so a
// read-only workspace can still be used for reading.
func Open(root *os.Root) (*Log, error) {
	if root == nil {
		return nil, errors.New("audit: nil root")
	}
	l := &Log{root: root, now: time.Now}
	if value := os.Getenv(AnchorEnv); value != "" {
		sum := sha256.Sum256([]byte(value))
		l.anchor = hex.EncodeToString(sum[:])
	}
	if err := l.loadChain(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Log) loadChain() error {
	f, err := l.root.Open(fileName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			l.prevHash = l.genesis()
			return nil
		}
		return fmt.Errorf("audit: read chain: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	lastHash := l.genesis()
	var lastSeq uint64
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return fmt.Errorf("audit: decode existing event: %w", err)
		}
		lastSeq++
		if event.Sequence != lastSeq || event.PrevHash != lastHash {
			return fmt.Errorf("audit: existing chain is broken at sequence %d", lastSeq)
		}
		want, err := hashEvent(event)
		if err != nil || want != event.Hash {
			return fmt.Errorf("audit: existing chain hash mismatch at sequence %d", lastSeq)
		}
		lastHash = event.Hash
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("audit: scan existing chain: %w", err)
	}
	l.prevHash, l.sequence = lastHash, lastSeq
	return nil
}

func (l *Log) genesis() string {
	if l.anchor == "" {
		return strings.Repeat("0", 64)
	}
	return l.anchor
}

func hashEvent(event Event) (string, error) {
	input := hashInput{
		Time: event.Time, Event: event.Event, Tool: event.Tool,
		ArgsSHA256: event.ArgsSHA256, ResultSHA256: event.ResultSHA256,
		Detail: event.Detail, Reason: event.Reason,
		Sequence: event.Sequence, PrevHash: event.PrevHash,
	}
	data, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(event.PrevHash+"|"), data...))
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
	event.Sequence = l.sequence + 1
	event.PrevHash = l.prevHash
	hash, err := hashEvent(event)
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

// Verify checks every sequence number and hash. It is meaningful only when
// the anchor is supplied from a source outside the log and the log is not
// writable by the party being audited.
func (l *Log) Verify() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.root.Open(fileName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	prev := l.genesis()
	var seq uint64
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return fmt.Errorf("audit: invalid event at line %d: %w", seq+1, err)
		}
		seq++
		if event.Sequence != seq || event.PrevHash != prev {
			return fmt.Errorf("audit: broken chain at line %d", seq)
		}
		want, err := hashEvent(event)
		if err != nil || want != event.Hash {
			return fmt.Errorf("audit: hash mismatch at line %d", seq)
		}
		prev = event.Hash
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if seq != l.sequence {
		return fmt.Errorf("audit: log has %d events, expected %d", seq, l.sequence)
	}
	return nil
}
