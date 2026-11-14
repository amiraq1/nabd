package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestF18PreviewApprovalState(t *testing.T) {
	dir := t.TempDir()
	content := []byte("old\n")
	mustWrite(t, filepath.Join(dir, "a.txt"), string(content), 0600)
	r := mustRegistry(t, dir)

	preview, err := r.PreviewApproval("edit_file", json.RawMessage(`{"path":"a.txt","old_text":"old","new_text":"new"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !preview.State.Exists {
		t.Fatal("expected Exists=true")
	}
	sum := sha256.Sum256(content)
	if preview.State.Hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("unexpected hash %q", preview.State.Hash)
	}
	if !strings.Contains(preview.Display, "-old") || !strings.Contains(preview.Display, "+new") {
		t.Fatalf("missing diff lines: %q", preview.Display)
	}
}

func TestF18PreviewApprovalMissingFile(t *testing.T) {
	r := mustRegistry(t, t.TempDir())
	preview, err := r.PreviewApproval("edit_file", json.RawMessage(`{"path":"missing.txt","old_text":"a","new_text":"b"}`))
	if err != nil {
		t.Fatal(err)
	}
	if preview.State.Exists {
		t.Fatal("expected Exists=false for a missing file")
	}
	if preview.State.Hash != "" {
		t.Fatalf("expected an empty hash, got %d chars", len(preview.State.Hash))
	}
	if !strings.Contains(preview.Display, "does not exist") {
		t.Fatalf("missing explicit notice: %q", preview.Display)
	}
}

func TestF18RunApprovedRejectsChangedFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	mustWrite(t, target, "old\n", 0600)
	r := mustRegistry(t, dir)

	args := json.RawMessage(`{"path":"a.txt","old_text":"old","new_text":"new"}`)
	preview, err := r.PreviewApproval("edit_file", args)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, target, "changed old\n", 0600)

	out := r.RunApproved(context.Background(), "edit_file", args, preview.State)
	if !strings.Contains(out, "file changed since approval") {
		t.Fatalf("missing hash mismatch error: %q", out)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "changed old\n" {
		t.Fatalf("file was overwritten: %q", got)
	}
}

func TestF18RunApprovedUnchangedFileSucceeds(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	mustWrite(t, target, "old\n", 0600)
	r := mustRegistry(t, dir)

	args := json.RawMessage(`{"path":"a.txt","old_text":"old","new_text":"new"}`)
	preview, err := r.PreviewApproval("edit_file", args)
	if err != nil {
		t.Fatal(err)
	}
	out := r.RunApproved(context.Background(), "edit_file", args, preview.State)
	if !strings.Contains(out, "Successfully updated") {
		t.Fatalf("edit failed: %q", out)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new\n" {
		t.Fatalf("unexpected content: %q", got)
	}
}
