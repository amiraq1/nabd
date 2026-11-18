package tools

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileLimitedAtAndOverLimit(t *testing.T) {
	root, reg := newTestRegistry(t)
	at := filepath.Join(root, "at.bin")
	if err := os.WriteFile(at, make([]byte, maxEditBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := reg.readFileLimited("at.bin", int64(maxEditBytes))
	if err != nil {
		t.Fatalf("read at limit: %v", err)
	}
	if len(data) != maxEditBytes {
		t.Fatalf("read %d bytes at limit, want %d", len(data), maxEditBytes)
	}

	over := filepath.Join(root, "over.bin")
	if err := os.WriteFile(over, make([]byte, maxEditBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.readFileLimited("over.bin", int64(maxEditBytes)); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("over-limit read error = %v", err)
	}
}

func TestFileDigestAtAndOverLimit(t *testing.T) {
	root, reg := newTestRegistry(t)
	at := filepath.Join(root, "at.bin")
	if err := os.WriteFile(at, make([]byte, maxEditBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := reg.FileDigest("at.bin")
	if err != nil {
		t.Fatalf("digest at limit: %v", err)
	}
	if len(digest) != 64 {
		t.Fatalf("digest length = %d, want 64", len(digest))
	}

	over := filepath.Join(root, "over.bin")
	if err := os.WriteFile(over, make([]byte, maxEditBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.FileDigest("over.bin"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("over-limit digest error = %v", err)
	}
}

func TestReadFileLimitedDetectsGrowth(t *testing.T) {
	root, reg := newTestRegistry(t)
	path := filepath.Join(root, "grow.txt")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}

	readBeforeLimitHook = func() {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Errorf("open for growth: %v", err)
			return
		}
		defer f.Close()
		if _, err := f.Write(bytes.Repeat([]byte("x"), 20)); err != nil {
			t.Errorf("append for growth: %v", err)
		}
	}
	defer func() { readBeforeLimitHook = nil }()

	data, err := reg.readFileLimited("grow.txt", 10)
	if err == nil || !strings.Contains(err.Error(), "read limit") {
		t.Fatalf("growth error = %v, data=%d bytes", err, len(data))
	}
	if data != nil {
		t.Fatalf("growth returned %d partial bytes", len(data))
	}
}

func TestFileDigestDetectsGrowth(t *testing.T) {
	root, reg := newTestRegistry(t)
	path := filepath.Join(root, "digest-grow.bin")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}

	readBeforeLimitHook = func() {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Errorf("open for growth: %v", err)
			return
		}
		defer f.Close()
		if _, err := f.Write(bytes.Repeat([]byte("x"), maxEditBytes)); err != nil {
			t.Errorf("append for growth: %v", err)
		}
	}
	defer func() { readBeforeLimitHook = nil }()

	if _, err := reg.FileDigest("digest-grow.bin"); err == nil || !strings.Contains(err.Error(), "read limit") {
		t.Fatalf("digest growth error = %v", err)
	}
}
