package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReliabilityGoVerifyBoundsOutput(t *testing.T) {
	requirePOSIXShell(t)
	root, r := newTestRegistry(t)
	r.allowUnsandboxedForTest = true
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf 'beginning\\n'\nhead -c 2000000 /dev/zero | tr '\\000' x\nprintf '\\nending\\n'\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_ = root
	out, err := r.runGoVerify(context.Background(), "./...", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"beginning", "ending", "bytes omitted"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %s", want)
		}
	}
}

func TestReliabilityListDirScanBound(t *testing.T) {
	root, r := newTestRegistry(t)
	for i := 0; i < 4100; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("entry%04d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := r.listDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "scan limit") {
		t.Fatal("listing did not report a bounded scan")
	}
}

func TestReliabilityEditStateRejectsOversizedRead(t *testing.T) {
	root, r := newTestRegistry(t)
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	p := editParams{Path: "a.txt", OldText: "old", NewText: "new"}
	preview, err := r.PreviewApproval("edit_file", []byte(`{"path":"a.txt","old_text":"old","new_text":"new"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, maxEditBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err = r.verifyEditState(p, preview.State)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatal("oversized approval recheck not rejected by size limit")
	}
}

func TestReliabilityFileDigestRejectsOversized(t *testing.T) {
	root, r := newTestRegistry(t)
	f, err := os.Create(filepath.Join(root, "big.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(maxEditBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	_, err = r.FileDigest("big.txt")
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatal("digest accepted an oversized file")
	}
}

func TestReliabilityBoundedOutputEdges(t *testing.T) {
	b := newBoundedOutput(8)
	_, _ = b.Write([]byte("1234"))
	_, _ = b.Write([]byte("5678"))
	if got := b.String(); got != "12345678" {
		t.Fatalf("exact boundary: %q", got)
	}
	_, _ = b.Write([]byte("9"))
	got := b.String()
	if !strings.HasPrefix(got, "1234") || !strings.HasSuffix(got, "6789") || !strings.Contains(got, "1 bytes omitted") {
		t.Fatalf("overflow: %q", got)
	}
	if len(b.head)+len(b.tail) > 8 {
		t.Fatal("retained more than the configured bound")
	}
}

func TestReliabilityBoundedReadEdges(t *testing.T) {
	root, r := newTestRegistry(t)
	name := filepath.Join(root, "edge.txt")
	if err := os.WriteFile(name, []byte("12345678"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := readBounded(r.root, "edge.txt", 8)
	if err != nil || string(data) != "12345678" {
		t.Fatal("exact boundary rejected")
	}
	if err := os.WriteFile(name, []byte("123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBounded(r.root, "edge.txt", 8); err == nil {
		t.Fatal("over-boundary input accepted")
	}
}

type countedReader struct {
	r *bytes.Reader
	n int
}

func (c *countedReader) Read(p []byte) (int, error) { n, err := c.r.Read(p); c.n += n; return n, err }
func TestReliabilityDigestStopsReadingAtLimit(t *testing.T) {
	c := &countedReader{r: bytes.NewReader(make([]byte, 64))}
	_, err := digestBounded(c, 8)
	if err == nil || c.n != 9 {
		t.Fatalf("digest read %d bytes; want limit+1 and an error", c.n)
	}
}
