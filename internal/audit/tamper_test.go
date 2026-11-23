package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	t.Setenv(AnchorEnv, "anchor-held-outside-the-log")
	log, _ := testLog(t)
	if err := log.Write(Event{Event: "approved", Tool: "edit_file"}); err != nil {
		t.Fatal(err)
	}
	if err := log.Verify(); err != nil {
		t.Fatalf("anchored chain failed verification: %v", err)
	}
	t.Setenv(AnchorEnv, "a-different-anchor")
	if _, err := Open(log.root); err == nil {
		t.Fatal("anchor change must invalidate the chain")
	}
}
