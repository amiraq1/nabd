package shadow

import (
	"os"
	"strings"
	"testing"
)

func TestReliabilityShadowTotalQuota(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.MkdirAll(baseDir, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := root.OpenFile(baseDir+"/orphan.bak", os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(128 << 20); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err = s.Backup("file.txt", []byte("a"), 0600, []byte("b")); err == nil || !strings.Contains(err.Error(), "quota") {
		t.Fatal("aggregate backup quota was not enforced")
	}
}
func TestReliabilityShadowOversizeRestore(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	s, _ := Open(root)
	entry, err := s.Backup("file.txt", []byte("a"), 0600, []byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := root.OpenFile(backupName(entry.ID), os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate((2 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, _, err = s.Latest("file.txt"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatal("oversized backup was not rejected at the read limit")
	}
}
