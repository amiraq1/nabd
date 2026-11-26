package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testLog(t *testing.T) (*Log, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	log, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return log, filepath.Join(dir, fileName)
}

func TestHashChainDetectsTampering(t *testing.T) {
	log, path := testLog(t)
	for _, event := range []Event{{Event: "requested", Tool: "edit_file"}, {Event: "applied", Tool: "edit_file"}} {
		if err := log.Write(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Verify(); err != nil {
		t.Fatalf("valid chain rejected: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	modified := strings.Replace(string(data), `"tool":"edit_file"`, `"tool":"read_file"`, 1)
	if modified == string(data) {
		t.Fatal("test did not modify the log")
	}
	if err := os.WriteFile(path, []byte(modified), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := log.Verify(); err == nil {
		t.Fatal("tampered event was not detected")
	}
}

func TestHashChainDetectsTruncation(t *testing.T) {
	log, path := testLog(t)
	for i := 0; i < 3; i++ {
		if err := log.Write(Event{Event: "requested", Tool: "read_file"}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if err := os.WriteFile(path, []byte(strings.Join(lines[:2], "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := log.Verify(); err == nil {
		t.Fatal("truncated chain was not detected")
	}
}

func TestAnchorIsOutsideTheLog(t *testing.T) {
	const secretAnchor = "anchor-held-outside-the-log"
	t.Setenv(AnchorEnv, secretAnchor)
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	log, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fileName)

	if err := log.Write(Event{Event: "approved", Tool: "edit_file"}); err != nil {
		t.Fatal(err)
	}
	if err := log.Verify(); err != nil {
		t.Fatalf("anchored chain failed verification: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rawStr := string(raw)
	anchorDigest := sha256.Sum256([]byte(secretAnchor))
	anchorHex := hex.EncodeToString(anchorDigest[:])
	if strings.Contains(rawStr, secretAnchor) || strings.Contains(rawStr, anchorHex) {
		t.Fatalf("audit log leaked anchor or derived anchor hash: %s", rawStr)
	}
	var first Event
	if err := json.Unmarshal([]byte(strings.TrimSpace(rawStr)), &first); err != nil {
		t.Fatal(err)
	}
	if first.Version != 1 || first.Mode != modeHMACSHA256 || first.PrevHash != strings.Repeat("0", 64) {
		t.Fatalf("unexpected v1 HMAC event header: %+v", first)
	}

	// Changing anchor must invalidate verification on reopen.
	t.Setenv(AnchorEnv, "a-different-anchor")
	if _, err := Open(root); err == nil {
		t.Fatal("anchor change must invalidate the chain")
	}

	// Stripping anchor env must not silently verify or downgrade an HMAC log.
	t.Setenv(AnchorEnv, "")
	if _, err := Open(root); err == nil {
		t.Fatal("missing anchor must fail closed when log is hmac-sha256")
	}
}

func TestAnchoredRejectsForgedAppendAndTruncationAndModeDowngrade(t *testing.T) {
	t.Setenv(AnchorEnv, "secret-hmac-key")
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	log, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range []Event{{Event: "requested", Tool: "edit_file"}, {Event: "applied", Tool: "edit_file"}} {
		if err := log.Write(ev); err != nil {
			t.Fatal(err)
		}
	}

	logPath := filepath.Join(dir, fileName)
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	var last Event
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatal(err)
	}

	// PoC: Attacker knows entire log contents (including last.Hash) but not NABD_AUDIT_ANCHOR.
	// Attempting to append a valid-looking unkeyed SHA-256 event (or HMAC with wrong key) must fail.
	forged := Event{
		Version:  1,
		Mode:     modeSHA256,
		Time:     time.Now().UTC(),
		Event:    "approved",
		Tool:     "mcp_call",
		Sequence: 3,
		PrevHash: last.Hash,
	}
	forgedLog := &Log{mode: modeSHA256}
	forged.Hash, err = forgedLog.computeEventHash(forged)
	if err != nil {
		t.Fatal(err)
	}
	forgedLine, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	tampered := string(raw) + string(forgedLine) + "\n"
	if err := os.WriteFile(logPath, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := log.Verify(); err == nil {
		t.Fatal("Verify accepted forged mode=sha256 appended event under HMAC anchor")
	}
	if _, err := Open(root); err == nil {
		t.Fatal("Open accepted forged mode=sha256 appended event under HMAC anchor")
	}

	// PoC: Rewriting whole log as mode="sha256" must fail closed with manual rotation guidance when anchor is set.
	downgraded := Event{
		Version:  1,
		Mode:     modeSHA256,
		Time:     time.Now().UTC(),
		Event:    "requested",
		Tool:     "read_file",
		Sequence: 1,
		PrevHash: strings.Repeat("0", 64),
	}
	downgraded.Hash, err = forgedLog.computeEventHash(downgraded)
	if err != nil {
		t.Fatal(err)
	}
	downLine, err := json.Marshal(downgraded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, append(downLine, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err == nil || !strings.Contains(err.Error(), "archive or rotate") {
		t.Fatalf("expected manual archive/rotate rejection on unkeyed log with anchor, got: %v", err)
	}
	// Existing file must NOT be auto-deleted by failed Open.
	if _, statErr := os.Stat(logPath); statErr != nil {
		t.Fatalf("rejected log file should be preserved on disk, got: %v", statErr)
	}
}

func TestAnchoredRejectsLegacyV0LogWithoutSilentDowngrade(t *testing.T) {
	t.Setenv(AnchorEnv, "anchor-secret")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nabd"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Construct a legacy v0 event using old sha256(anchor) prev_hash genesis style.
	ancSum := sha256.Sum256([]byte("anchor-secret"))
	legacy := Event{
		Time:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Event:    "requested",
		Tool:     "read_file",
		Sequence: 1,
		PrevHash: hex.EncodeToString(ancSum[:]),
	}
	unkeyed := &Log{mode: modeSHA256}
	h, err := unkeyed.computeEventHash(legacy)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Hash = h
	line, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, fileName)
	if err := os.WriteFile(logPath, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	if _, err := Open(root); err == nil || !strings.Contains(err.Error(), "legacy v0") || !strings.Contains(err.Error(), "archive or rotate") {
		t.Fatalf("expected explicit legacy v0 archive/rotate error, got: %v", err)
	}
	// Preserve original file (no deletion or rewrite).
	after, err := os.ReadFile(logPath)
	if err != nil || string(after) != string(append(line, '\n')) {
		t.Fatalf("legacy v0 file was altered or deleted: err=%v", err)
	}
}

func TestUnanchoredLegacyV0LogStillVerifies(t *testing.T) {
	t.Setenv(AnchorEnv, "")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nabd"), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := Event{
		Time:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Event:    "requested",
		Tool:     "read_file",
		Sequence: 1,
		PrevHash: strings.Repeat("0", 64),
	}
	unkeyed := &Log{mode: modeSHA256}
	var err error
	legacy.Hash, err = unkeyed.computeEventHash(legacy)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	log, err := Open(root)
	if err != nil {
		t.Fatalf("expected unanchored v0 log to load without anchor: %v", err)
	}
	if err := log.Write(Event{Event: "applied", Tool: "read_file"}); err != nil {
		t.Fatal(err)
	}
	if err := log.Verify(); err != nil {
		t.Fatalf("Verify after appending v1 to unanchored v0 failed: %v", err)
	}
}
