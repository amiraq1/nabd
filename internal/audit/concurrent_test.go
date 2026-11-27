package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain doubles as the child-process entry point for the cross-process
// tests, so no separate helper binary needs to be built or committed.
func TestMain(m *testing.M) {
	if os.Getenv("NABD_AUDIT_HELPER") == "1" {
		os.Exit(runAuditHelper())
	}
	os.Exit(m.Run())
}

func runAuditHelper() int {
	rootDir := os.Getenv("NABD_AUDIT_ROOT")
	prefix := os.Getenv("NABD_AUDIT_PREFIX")
	barrier := os.Getenv("NABD_AUDIT_BARRIER")
	count, err := strconv.Atoi(os.Getenv("NABD_AUDIT_COUNT"))
	if err != nil || rootDir == "" || prefix == "" || barrier == "" {
		fmt.Fprintln(os.Stderr, "bad helper environment")
		return 2
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer root.Close()
	log, err := Open(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := os.WriteFile(filepath.Join(barrier, "ready-"+prefix), []byte("1"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, statErr := os.Stat(filepath.Join(barrier, "go")); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "barrier timeout")
			return 2
		}
		time.Sleep(2 * time.Millisecond)
	}
	for i := 0; i < count; i++ {
		if err := log.Write(Event{Event: "requested", Tool: prefix, Detail: fmt.Sprintf("%s-%d", prefix, i)}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err := log.Verify(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func TestConcurrentProcessesShareOneChain(t *testing.T) {
	for _, mode := range []string{modeSHA256, modeHMACSHA256} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			barrier := t.TempDir()
			if mode == modeHMACSHA256 {
				t.Setenv(AnchorEnv, "concurrent-test-anchor")
			} else {
				t.Setenv(AnchorEnv, "")
			}

			const processes = 2
			const perProcess = 20
			type child struct {
				cmd  *exec.Cmd
				out  bytes.Buffer
				done chan error
			}
			children := make([]*child, 0, processes)
			prefixes := []string{"alpha", "beta"}
			for i := 0; i < processes; i++ {
				c := &child{done: make(chan error, 1)}
				c.cmd = exec.Command(os.Args[0])
				c.cmd.Env = append(os.Environ(),
					"NABD_AUDIT_HELPER=1",
					"NABD_AUDIT_ROOT="+dir,
					"NABD_AUDIT_PREFIX="+prefixes[i],
					"NABD_AUDIT_BARRIER="+barrier,
					"NABD_AUDIT_COUNT="+strconv.Itoa(perProcess),
				)
				c.cmd.Stdout = &c.out
				c.cmd.Stderr = &c.out
				if err := c.cmd.Start(); err != nil {
					t.Fatalf("start helper %d: %v", i, err)
				}
				children = append(children, c)
				go func(c *child) { c.done <- c.cmd.Wait() }(c)
			}

			// Start barrier: both children are holding an open Log before any
			// append, which is exactly the case the lock must serialize.
			deadline := time.Now().Add(20 * time.Second)
			for {
				ready := 0
				for _, prefix := range prefixes {
					if _, err := os.Stat(filepath.Join(barrier, "ready-"+prefix)); err == nil {
						ready++
					}
				}
				if ready == processes {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("helpers did not become ready")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err := os.WriteFile(filepath.Join(barrier, "go"), []byte("1"), 0o600); err != nil {
				t.Fatal(err)
			}

			for i, c := range children {
				select {
				case err := <-c.done:
					if err != nil {
						t.Fatalf("helper %d failed: %v\n%s", i, err, c.out.String())
					}
				case <-time.After(30 * time.Second):
					_ = c.cmd.Process.Kill()
					t.Fatalf("helper %d timed out\n%s", i, c.out.String())
				}
			}

			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			reopened, err := Open(root)
			if err != nil {
				t.Fatalf("reopen failed: %v", err)
			}
			if err := reopened.Verify(); err != nil {
				t.Fatalf("verify after concurrent writes: %v", err)
			}

			raw, err := os.ReadFile(filepath.Join(dir, fileName))
			if err != nil {
				t.Fatal(err)
			}
			scanner := bufio.NewScanner(bytes.NewReader(raw))
			scanner.Buffer(make([]byte, 64<<10), 1<<20)
			var seqs []uint64
			tools := map[string]int{}
			for scanner.Scan() {
				var event Event
				if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
					t.Fatalf("invalid line: %v", err)
				}
				seqs = append(seqs, event.Sequence)
				tools[event.Tool]++
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			if want := processes * perProcess; len(seqs) != want {
				t.Fatalf("got %d events, want %d", len(seqs), want)
			}
			for i, seq := range seqs {
				if seq != uint64(i+1) {
					t.Fatalf("sequence at line %d = %d, want %d", i, seq, i+1)
				}
			}
			for _, prefix := range prefixes {
				if tools[prefix] != perProcess {
					t.Fatalf("tool %q recorded %d times, want %d", prefix, tools[prefix], perProcess)
				}
			}
			if reopened.sequence != uint64(processes*perProcess) {
				t.Fatalf("in-memory sequence = %d, want %d", reopened.sequence, processes*perProcess)
			}
		})
	}
}

func writeSome(t *testing.T, log *Log, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := log.Write(Event{Event: "requested", Tool: "edit_file", Detail: fmt.Sprintf("e-%d", i)}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
}

func TestWriteRefusesTruncationReplacementAndPartialTail(t *testing.T) {
	log, path := testLog(t)
	writeSome(t, log, 2)

	truncated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimRight(truncated, "\n"), []byte("\n"))
	if err := os.WriteFile(path, append(lines[0], '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := log.Write(Event{Event: "applied", Tool: "edit_file"}); err == nil {
		t.Fatal("append after truncation was accepted")
	}

	// Restore a full two-event chain, then replace the file (new inode) while a
	// Log still holds the old identity.
	log2, path2 := testLog(t)
	writeSome(t, log2, 1)
	original, err := os.ReadFile(path2)
	if err != nil {
		t.Fatal(err)
	}
	replacement := path2 + ".replacement"
	if err := os.WriteFile(replacement, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path2); err != nil {
		t.Fatal(err)
	}
	if err := log2.Write(Event{Event: "applied", Tool: "edit_file"}); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("append after replacement = %v, want identity rejection", err)
	}

	// A half-written record appended by another process must never be accepted
	// or extended.
	log3, path3 := testLog(t)
	writeSome(t, log3, 1)
	f, err := os.OpenFile(path3, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"v":1,"event":"applied"`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := log3.Write(Event{Event: "applied", Tool: "edit_file"}); err == nil {
		t.Fatal("append after a partial trailing record was accepted")
	}
}

func TestWriteDetectsPrefixRewriteWhenMetadataChanges(t *testing.T) {
	log, path := testLog(t)
	writeSome(t, log, 2)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	modified := bytes.Replace(raw, []byte(`"detail":"e-0"`), []byte(`"detail":"x-0"`), 1)
	if bytes.Equal(raw, modified) {
		t.Fatal("test did not modify the prefix")
	}
	if err := os.WriteFile(path, modified, 0o600); err != nil {
		t.Fatal(err)
	}
	// Writing changes mtime, which forces the full re-verification path.
	if err := log.Write(Event{Event: "applied", Tool: "edit_file"}); err == nil {
		t.Fatal("prefix rewrite with a changed mtime was accepted")
	}
}

func TestWriteReportsDirectorySyncFailureButKeepsChainConsistent(t *testing.T) {
	log, _ := testLog(t)
	injected := errors.New("injected: directory sync failed")
	log.syncDir = func(*os.File) error { return injected }
	err := log.Write(Event{Event: "requested", Tool: "edit_file"})
	if !errors.Is(err, injected) {
		t.Fatalf("Write = %v, want injected directory sync error", err)
	}
	// The record was written before the directory sync, so the in-memory chain
	// advanced; a retry must not duplicate the sequence number.
	log.syncDir = func(f *os.File) error { return f.Sync() }
	if err := log.Write(Event{Event: "applied", Tool: "edit_file"}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if err := log.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestWriteReportsFileSyncFailureAndRecovers(t *testing.T) {
	log, _ := testLog(t)
	injected := errors.New("injected: file sync failed")
	log.syncFile = func(*os.File) error { return injected }
	if err := log.Write(Event{Event: "requested", Tool: "edit_file"}); !errors.Is(err, injected) {
		t.Fatalf("Write = %v, want injected file sync error", err)
	}
	log.syncFile = func(f *os.File) error { return f.Sync() }
	// The previous record may or may not be durable, but if it is present it is
	// a complete valid record; the next append must verify and continue it.
	if err := log.Write(Event{Event: "applied", Tool: "edit_file"}); err != nil {
		t.Fatalf("write after sync failure: %v", err)
	}
	if err := log.Verify(); err != nil {
		t.Fatalf("verify after recovery: %v", err)
	}
}

func TestWriteFailsClosedWhenAuditLockIsHeld(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	log, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	log.lockWait = 100 * time.Millisecond

	held, err := acquireAuditLock(root, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Write(Event{Event: "requested", Tool: "edit_file"}); err == nil {
		t.Fatal("Write succeeded while another holder had the audit lock")
	}
	if err := held.close(); err != nil {
		t.Fatal(err)
	}
	if err := log.Write(Event{Event: "requested", Tool: "edit_file"}); err != nil {
		t.Fatalf("write after lock release: %v", err)
	}
}
