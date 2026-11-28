package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func openTestStore(t *testing.T) (string, *Store) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return dir, s
}

func TestOpenAndReadDoNotCreateNabdDir(t *testing.T) {
	dir, s := openTestStore(t)

	entries, total, err := s.List(context.Background(), 10)
	if err != nil || total != 0 || len(entries) != 0 {
		t.Fatalf("List on empty workspace failed: entries=%v total=%d err=%v", entries, total, err)
	}
	_, raw, err := s.Search(context.Background(), "anything", "", nil)
	if err != nil || len(raw) == 0 {
		t.Fatalf("Search on empty workspace failed: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".nabd")); !os.IsNotExist(err) {
		t.Fatalf("read-only operations created .nabd directory (err=%v)", err)
	}
}

func TestSaveSearchDeleteAndCapacityReclaim(t *testing.T) {
	dir, s := openTestStore(t)
	ctx := context.Background()

	e1, err := s.Save(ctx, SaveInput{
		Kind:  "decision",
		Title: "Use Okapi BM25",
		Body:  "Implement local BM25 search without external dependencies.",
		Tags:  []string{"bm25", "search", "bm25"},
	})
	if err != nil {
		t.Fatalf("Save 1: %v", err)
	}
	if err := ValidateID(e1.ID); err != nil {
		t.Fatalf("invalid generated ID: %v", err)
	}
	if len(e1.Tags) != 2 || e1.Tags[0] != "bm25" || e1.Tags[1] != "search" {
		t.Fatalf("expected sorted deduplicated tags, got %v", e1.Tags)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, ".nabd", "memory", "entries.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("insecure file permissions: %04o", info.Mode().Perm())
		}
	}

	resp, _, err := s.Search(ctx, "Okapi BM25", "", nil)
	if err != nil || resp.Returned != 1 || resp.Results[0].ID != e1.ID {
		t.Fatalf("Search failed: resp=%+v err=%v", resp, err)
	}

	removed, err := s.Delete(ctx, e1.ID)
	if err != nil || removed.ID != e1.ID {
		t.Fatalf("Delete failed: removed=%+v err=%v", removed, err)
	}
	entries, total, err := s.List(ctx, 10)
	if err != nil || total != 0 || len(entries) != 0 {
		t.Fatalf("expected empty store after delete, got total=%d len=%d err=%v", total, len(entries), err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".nabd", "memory", "entries.jsonl"))
	if err != nil || len(data) != 0 {
		t.Fatalf("expected 0-byte entries.jsonl after deleting sole entry, got %d bytes err=%v", len(data), err)
	}
}

func TestLimitsAndBoundaryValues(t *testing.T) {
	_, s := openTestStore(t)
	ctx := context.Background()

	// Exact max title (120B), exact max body (2048B), exact 8 tags of 32 chars.
	title120 := strings.Repeat("a", MaxTitleBytes)
	body2048 := strings.Repeat("b", MaxBodyBytes)
	tags8 := make([]string, MaxTags)
	for i := 0; i < MaxTags; i++ {
		tags8[i] = fmt.Sprintf("tag%d_%s", i, strings.Repeat("x", MaxTagBytes-5))
	}
	if _, err := s.Save(ctx, SaveInput{Title: title120, Body: body2048, Tags: tags8}); err != nil {
		t.Fatalf("Save at exact boundaries failed: %v", err)
	}

	badInputs := []struct {
		name string
		in   SaveInput
	}{
		{"title_121", SaveInput{Title: strings.Repeat("a", MaxTitleBytes+1), Body: "ok"}},
		{"body_2049", SaveInput{Title: "ok", Body: strings.Repeat("b", MaxBodyBytes+1)}},
		{"tags_9", SaveInput{Title: "ok", Body: "ok", Tags: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}}},
		{"tag_bad_chars", SaveInput{Title: "ok", Body: "ok", Tags: []string{"BadTag"}}},
		{"tag_33", SaveInput{Title: "ok", Body: "ok", Tags: []string{strings.Repeat("a", MaxTagBytes+1)}}},
		{"title_multiline", SaveInput{Title: "line1\nline2", Body: "ok"}},
		{"title_bidi", SaveInput{Title: "ok\u202espoof", Body: "ok"}},
		{"body_zwsp", SaveInput{Title: "ok", Body: "hidden\u200bchar"}},
		{"body_nul", SaveInput{Title: "ok", Body: "nul\x00byte"}},
		{"bad_kind", SaveInput{Kind: "other", Title: "ok", Body: "ok"}},
	}
	for _, tc := range badInputs {
		if _, err := s.Save(ctx, tc.in); err == nil {
			t.Errorf("%s unexpectedly succeeded", tc.name)
		}
	}
}

func TestStrictJSONRejectsUnknownDuplicateAndTrailing(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"unknown_field", `{"title":"a","body":"b","extra":1}`},
		{"duplicate_key", `{"title":"a","title":"c","body":"b"}`},
		{"trailing_json", `{"title":"a","body":"b"}{"x":1}`},
		{"wrong_type", `{"title":123,"body":"b"}`},
		{"null_payload", `null`},
	}
	for _, tc := range cases {
		var in SaveInput
		if err := DecodeStrictJSONObject([]byte(tc.raw), &in); err == nil {
			t.Errorf("%s: expected rejection", tc.name)
		}
	}
}

func TestMaxEntries500And2MiBCap(t *testing.T) {
	dir, s := openTestStore(t)
	ctx := context.Background()

	// Seed 500 valid entries directly in entries.jsonl to test 500/501 cap fast.
	var seed []Entry
	for i := 0; i < MaxEntries; i++ {
		seed = append(seed, Entry{
			ID:        fmt.Sprintf("mem_%08x", i+1),
			Kind:      KindNote,
			Title:     fmt.Sprintf("Entry %d", i),
			Body:      "Body text",
			CreatedAt: "2026-11-28T12:00:00Z",
		})
	}
	payload, err := encodeEntriesJSONL(seed)
	if err != nil {
		t.Fatal(err)
	}
	memDir := filepath.Join(dir, ".nabd", "memory")
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "entries.jsonl"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Save(ctx, SaveInput{Title: "Entry 501", Body: "Over limit"}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("expected ErrLimitExceeded at 500 entries, got %v", err)
	}

	// Delete one entry and verify Save succeeds (capacity reclaimed).
	if _, err := s.Delete(ctx, "mem_00000001"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := s.Save(ctx, SaveInput{Title: "Replacement", Body: "Fits after delete"}); err != nil {
		t.Fatalf("Save after delete failed: %v", err)
	}

	// Test 2 MiB file size cap with fewer than 500 entries via JSON escaping expansion.
	// Each entry with 2048 '<' characters escapes to '\u003c' (6x -> ~12.3 KiB),
	// so ~170 entries (< 500) fills just under 2 MiB; the next Save must fail.
	escapedBody := strings.Repeat("<", MaxBodyBytes)
	var near2MiB []Entry
	var approxBytes int
	for i := 0; i < MaxEntries-5; i++ {
		e := Entry{
			ID:        fmt.Sprintf("mem_%08x", i+1000),
			Kind:      KindNote,
			Title:     "Escaped",
			Body:      escapedBody,
			CreatedAt: "2026-11-28T12:00:00Z",
		}
		lineBytes, _ := encodeEntriesJSONL([]Entry{e})
		if approxBytes+len(lineBytes) > MaxFileBytes {
			break
		}
		near2MiB = append(near2MiB, e)
		approxBytes += len(lineBytes)
	}
	nearPayload, err := encodeEntriesJSONL(near2MiB)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "entries.jsonl"), nearPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	if len(near2MiB) >= MaxEntries {
		t.Fatalf("expected fewer than %d entries to test 2 MiB cap independently, got %d", MaxEntries, len(near2MiB))
	}
	if _, err := s.Save(ctx, SaveInput{Title: "Push over 2MiB", Body: escapedBody}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("expected ErrLimitExceeded when JSON-escaped save exceeds 2 MiB (near=%d), got %v", len(nearPayload), err)
	}

	// Test reading a file externally padded over 2 MiB fails closed.
	oversized := bytes.Repeat([]byte("a"), MaxFileBytes+1)
	if err := os.WriteFile(filepath.Join(memDir, "entries.jsonl"), oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.List(ctx, 10); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("expected ErrLimitExceeded for >2MiB entries.jsonl, got %v", err)
	}
}

func TestIDCollisionRetryAndExhaustion(t *testing.T) {
	_, s := openTestStore(t)
	ctx := context.Background()

	// Deterministic rand bytes: always return 0x00000001 on first Save, then succeed on second Save after 1 collision.
	calls := 0
	s.randRead = func(b []byte) (int, error) {
		calls++
		if calls <= 2 {
			copy(b, []byte{0x00, 0x00, 0x00, 0x01})
			return len(b), nil
		}
		copy(b, []byte{0x00, 0x00, 0x00, 0x02})
		return len(b), nil
	}

	e1, err := s.Save(ctx, SaveInput{Title: "First", Body: "Body 1"})
	if err != nil || e1.ID != "mem_00000001" {
		t.Fatalf("first Save failed: %+v err=%v", e1, err)
	}
	e2, err := s.Save(ctx, SaveInput{Title: "Second", Body: "Body 2"})
	if err != nil || e2.ID != "mem_00000002" {
		t.Fatalf("collision retry Save failed: %+v err=%v", e2, err)
	}

	// Exhaustion: always return existing ID 0x00000001.
	s.randRead = func(b []byte) (int, error) {
		copy(b, []byte{0x00, 0x00, 0x00, 0x01})
		return len(b), nil
	}
	if _, err := s.Save(ctx, SaveInput{Title: "Third", Body: "Body 3"}); err == nil {
		t.Fatal("expected ID collision exhaustion error")
	}
}

func TestCorruptTruncatedAndDurabilityFailureHandling(t *testing.T) {
	dir, s := openTestStore(t)
	ctx := context.Background()

	e1, err := s.Save(ctx, SaveInput{Title: "Valid", Body: "Original intact data"})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Mid-write failure before rename must leave original entries.jsonl intact.
	s.writeHook = func(f *os.File, _ []byte) error {
		_, _ = f.Write([]byte("partial-garbage"))
		return errors.New("simulated disk write error")
	}
	if _, err := s.Save(ctx, SaveInput{Title: "Should Fail", Body: "Never committed"}); err == nil {
		t.Fatal("expected write failure")
	}
	s.writeHook = nil

	got, err := s.Get(ctx, e1.ID)
	if err != nil || got.Title != "Valid" {
		t.Fatalf("original entry damaged after failed write: %+v err=%v", got, err)
	}

	// 2. Directory sync failure returns CommitUncertainError without hiding write.
	s.syncDir = func(*os.File) error { return errors.New("simulated dir sync EIO") }
	e2, err := s.Save(ctx, SaveInput{Title: "Uncertain", Body: "Written before sync fail"})
	var uncertain *CommitUncertainError
	if !errors.As(err, &uncertain) {
		t.Fatalf("expected CommitUncertainError, got %v", err)
	}
	s.syncDir = syncDirectoryFD
	if fetched, getErr := s.Get(ctx, e2.ID); getErr != nil || fetched.Title != "Uncertain" {
		t.Fatalf("expected written entry to remain readable, got %+v err=%v", fetched, getErr)
	}

	// 3. Corrupt / truncated JSONL fails closed without wiping data.
	entriesPath := filepath.Join(dir, ".nabd", "memory", "entries.jsonl")
	origData, err := os.ReadFile(entriesPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entriesPath, append(origData, []byte(`{"id":"mem_deadbeef"`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.List(ctx, 10); !errors.Is(err, ErrCorruptStore) {
		t.Fatalf("expected ErrCorruptStore for truncated trailing line, got %v", err)
	}
	if _, err := s.Save(ctx, SaveInput{Title: "Blocked", Body: "Must fail closed"}); !errors.Is(err, ErrCorruptStore) {
		t.Fatalf("expected Save to fail closed on corrupt store, got %v", err)
	}
	afterFail, err := os.ReadFile(entriesPath)
	if err != nil || len(afterFail) <= len(origData) {
		t.Fatal("corrupt store file was unexpectedly wiped or modified")
	}
}

func TestSecretDetectionRejectsWithoutWritingFiles(t *testing.T) {
	dir, s := openTestStore(t)
	ctx := context.Background()

	fakeSK := "sk-" + strings.Repeat("X", 20)
	fakeAIza := "AIza" + strings.Repeat("Z", 20)
	fakeGHP := "ghp_" + strings.Repeat("A", 24)
	fakeFC := "fc-" + strings.Repeat("Y", 20)
	fakePEM := "-----BEGIN RSA " + "PRIVATE KEY-----"
	fakeJWT := "eyJ" + "hbGciOiJIUzI1NiJ9." + "eyJzdWIiOiIxIn0." + "c2lnbmF0dXJlMTIzNDU2"

	for _, secret := range []string{fakeSK, fakeAIza, fakeGHP, fakeFC, fakePEM, fakeJWT} {
		_, err := s.Save(ctx, SaveInput{
			Title: "Note with token",
			Body:  "value: " + secret,
		})
		if !errors.Is(err, ErrSecretDetected) {
			t.Fatalf("expected ErrSecretDetected for secret pattern, got %v", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatal("error message leaked secret value")
		}
	}

	if _, err := os.Stat(filepath.Join(dir, ".nabd", "memory", "entries.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("entries.jsonl was created for rejected secret (err=%v)", err)
	}
}

func TestSymlinkAndNonRegularFilesRejected(t *testing.T) {
	dir, s := openTestStore(t)
	ctx := context.Background()
	outside := t.TempDir()

	if err := os.Symlink(outside, filepath.Join(dir, ".nabd")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if _, err := s.Save(ctx, SaveInput{Title: "Bad", Body: "Symlink .nabd"}); err == nil {
		t.Fatal("accepted symlinked .nabd directory")
	}
	if _, _, err := s.List(ctx, 5); err == nil {
		t.Fatal("List accepted symlinked .nabd directory")
	}
}

func TestCrossProcessLockingTimeoutAndCancellation(t *testing.T) {
	dir, s := openTestStore(t)
	ctx := context.Background()

	// 1. Two independent OS processes concurrently saving entries must lose zero writes.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd1 := exec.Command(exe, "-test.run=TestMemoryLockHelperProcess")
	cmd1.Env = append(os.Environ(), "NABD_MEM_TEST_MODE=save_batch", "NABD_MEM_TEST_DIR="+dir, "NABD_MEM_TEST_PREFIX=p1")
	cmd2 := exec.Command(exe, "-test.run=TestMemoryLockHelperProcess")
	cmd2.Env = append(os.Environ(), "NABD_MEM_TEST_MODE=save_batch", "NABD_MEM_TEST_DIR="+dir, "NABD_MEM_TEST_PREFIX=p2")

	if err := cmd1.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd2.Start(); err != nil {
		_ = cmd1.Process.Kill()
		t.Fatal(err)
	}
	err1 := cmd1.Wait()
	err2 := cmd2.Wait()
	if err1 != nil || err2 != nil {
		t.Fatalf("concurrent child processes failed: err1=%v err2=%v", err1, err2)
	}

	entries, total, err := s.List(ctx, 0)
	if err != nil {
		t.Fatalf("List after concurrent saves: %v", err)
	}
	if total != 10 || len(entries) != 10 {
		t.Fatalf("expected 10 entries from 2 concurrent processes (5 each), got total=%d len=%d", total, len(entries))
	}

	// 1b. Concurrent save and delete across two independent OS processes must
	// preserve non-deleted entries and lose no new writes.
	delTarget1 := entries[0].ID
	delTarget2 := entries[1].ID
	delCmd := exec.Command(exe, "-test.run=TestMemoryLockHelperProcess")
	delCmd.Env = append(os.Environ(),
		"NABD_MEM_TEST_MODE=delete_ids",
		"NABD_MEM_TEST_DIR="+dir,
		"NABD_MEM_TEST_IDS="+delTarget1+","+delTarget2,
	)
	saveCmd := exec.Command(exe, "-test.run=TestMemoryLockHelperProcess")
	saveCmd.Env = append(os.Environ(),
		"NABD_MEM_TEST_MODE=save_batch",
		"NABD_MEM_TEST_DIR="+dir,
		"NABD_MEM_TEST_PREFIX=p3",
	)
	if err := delCmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := saveCmd.Start(); err != nil {
		_ = delCmd.Process.Kill()
		t.Fatal(err)
	}
	if errA, errB := delCmd.Wait(), saveCmd.Wait(); errA != nil || errB != nil {
		t.Fatalf("concurrent delete/save processes failed: del=%v save=%v", errA, errB)
	}
	afterConcurrent, totalAfter, err := s.List(ctx, 0)
	if err != nil {
		t.Fatalf("List after concurrent delete+save: %v", err)
	}
	if totalAfter != 13 || len(afterConcurrent) != 13 {
		t.Fatalf("expected 13 entries (10 - 2 + 5), got total=%d len=%d", totalAfter, len(afterConcurrent))
	}
	for _, e := range afterConcurrent {
		if e.ID == delTarget1 || e.ID == delTarget2 {
			t.Fatalf("deleted entry %s reappeared after concurrent save", e.ID)
		}
	}

	// 2. Lock timeout & context cancellation while another OS process holds memory.lock.
	holdCmd := exec.Command(exe, "-test.run=TestMemoryLockHelperProcess")
	holdCmd.Env = append(os.Environ(), "NABD_MEM_TEST_MODE=hold_lock", "NABD_MEM_TEST_DIR="+dir)
	stdout, err := holdCmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holdCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = holdCmd.Process.Kill()
		_ = holdCmd.Wait()
	}()

	ready := make([]byte, 6)
	if _, err := io.ReadFull(stdout, ready); err != nil {
		t.Fatalf("failed waiting for lock holder ready signal: %v", err)
	}

	// Timeout test with short lockWait.
	s.lockWait = 120 * time.Millisecond
	if _, err := s.Save(ctx, SaveInput{Title: "Blocked", Body: "Should timeout"}); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("expected ErrLockBusy on lock timeout, got %v", err)
	}

	// Context cancellation test while lock is held.
	s.lockWait = DefaultLockWait
	cCtx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer cancel()
	if _, err := s.Save(cCtx, SaveInput{Title: "Canceled", Body: "Should cancel"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
}

func TestMemoryLockHelperProcess(t *testing.T) {
	mode := os.Getenv("NABD_MEM_TEST_MODE")
	if mode == "" {
		return
	}
	dir := os.Getenv("NABD_MEM_TEST_DIR")
	root, err := os.OpenRoot(dir)
	if err != nil {
		os.Exit(2)
	}
	defer root.Close()

	switch mode {
	case "save_batch":
		s, err := Open(root)
		if err != nil {
			os.Exit(3)
		}
		prefix := os.Getenv("NABD_MEM_TEST_PREFIX")
		for i := 0; i < 5; i++ {
			if _, err := s.Save(context.Background(), SaveInput{
				Title: fmt.Sprintf("%s-%d", prefix, i),
				Body:  fmt.Sprintf("concurrent entry %s %d", prefix, i),
			}); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(4)
			}
		}
		os.Exit(0)
	case "delete_ids":
		s, err := Open(root)
		if err != nil {
			os.Exit(3)
		}
		for _, id := range strings.Split(os.Getenv("NABD_MEM_TEST_IDS"), ",") {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, err := s.Delete(context.Background(), id); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(7)
			}
		}
		os.Exit(0)
	case "hold_lock":
		h, err := openMemoryDir(root, true)
		if err != nil {
			os.Exit(5)
		}
		defer h.close()
		lock, err := acquireMemoryLock(context.Background(), h, DefaultLockWait)
		if err != nil {
			os.Exit(6)
		}
		defer func() { _ = lock.close() }()
		_, _ = os.Stdout.Write([]byte("LOCKED"))
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}
}
