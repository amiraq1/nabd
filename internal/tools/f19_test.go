package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestF19ChangeBeforeRenameRejected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	mustWrite(t, target, "old\n", 0600)
	r := mustRegistry(t, dir)

	args := json.RawMessage(`{"path":"a.txt","old_text":"old","new_text":"new"}`)
	preview, err := r.PreviewApproval("edit_file", args)
	if err != nil {
		t.Fatal(err)
	}

	editBeforeRenameHook = func() {
		if err := os.WriteFile(target, []byte("raced\n"), 0600); err != nil {
			t.Errorf("hook write: %v", err)
		}
	}
	defer func() { editBeforeRenameHook = nil }()

	out := r.RunApproved(context.Background(), "edit_file", args, preview.State)
	if !strings.Contains(out, "file changed since approval") {
		t.Fatalf("missing pre-rename mismatch error: %q", out)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "raced\n" {
		t.Fatalf("file was overwritten after a pre-rename change: %q", got)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v", matches)
	}
}

func TestF19PathMismatchRejected(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	mustWrite(t, a, "old\n", 0600)
	mustWrite(t, b, "old\n", 0600)
	r := mustRegistry(t, dir)

	argsA := json.RawMessage(`{"path":"a.txt","old_text":"old","new_text":"new"}`)
	argsB := json.RawMessage(`{"path":"b.txt","old_text":"old","new_text":"new"}`)
	previewA, err := r.PreviewApproval("edit_file", argsA)
	if err != nil {
		t.Fatal(err)
	}

	out := r.RunApproved(context.Background(), "edit_file", argsB, previewA.State)
	if !strings.Contains(out, "path mismatch") {
		t.Fatalf("missing path mismatch error: %q", out)
	}
	gotA, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotA) != "old\n" || string(gotB) != "old\n" {
		t.Fatalf("a file was modified: a=%q b=%q", gotA, gotB)
	}
}

func TestF19RunWithApproverDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	mustWrite(t, target, "old\n", 0600)
	r, err := NewRegistry(dir, func(string, string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}

	args := json.RawMessage(`{"path":"a.txt","old_text":"old","new_text":"new"}`)
	out := r.Run(context.Background(), "edit_file", args)
	if !strings.Contains(out, "requires approval") {
		t.Fatalf("Run did not fail closed: %q", out)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old\n" {
		t.Fatalf("Run wrote despite the fail-closed rule: %q", got)
	}
}
