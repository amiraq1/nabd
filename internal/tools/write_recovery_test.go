package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func editArgs(path, oldText, newText string) map[string]any {
	return map[string]any{"path": path, "old_text": oldText, "new_text": newText}
}

func approvedEdit(t *testing.T, r *Registry, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	token, err := r.Approve("edit_file", raw)
	if err != nil {
		t.Fatal(err)
	}
	return r.RunApproved(context.Background(), "edit_file", raw, token)
}

func TestCrossProcessLockRefusesConcurrentEdit(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "old\n", 0o600)
	r := mustRegistry(t, dir)
	defer r.Close()

	lock, err := acquireProcessLock(r.root)
	if err != nil {
		t.Fatal(err)
	}
	out := approvedEdit(t, r, editArgs("a.txt", "old", "new"))
	if !strings.Contains(out, "locked by another process") {
		t.Fatalf("concurrent edit was not refused: %s", out)
	}
	if err := lock.close(); err != nil {
		t.Fatal(err)
	}
	if out := approvedEdit(t, r, editArgs("a.txt", "old", "new")); !strings.Contains(out, "Successfully") {
		t.Fatalf("edit after release failed: %s", out)
	}
}

func TestDirectorySyncFailureKeepsRestoreAvailable(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "old\n", 0o600)
	r := mustRegistry(t, dir)
	defer r.Close()

	old := syncParentDirectoryHook
	syncParentDirectoryHook = func(*os.File) error { return errors.New("injected directory sync failure") }
	t.Cleanup(func() { syncParentDirectoryHook = old })

	out := approvedEdit(t, r, editArgs("a.txt", "old", "new"))
	if !strings.Contains(out, "Successfully") || !strings.Contains(out, "directory sync failed") {
		t.Fatalf("commit uncertainty was not reported: %s", out)
	}
	restored, err := r.Undo("a.txt")
	if err != nil || !strings.Contains(restored, "Restored") {
		t.Fatalf("shadow restore was lost after sync failure: %q %v", restored, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(data) != "old\n" {
		t.Fatalf("restore bytes changed: %q %v", data, err)
	}
}

func TestReplacementFailureLeavesOriginalAndNoTemp(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "old\n", 0o600)
	r := mustRegistry(t, dir)
	defer r.Close()

	old := parentRenameHook
	parentRenameHook = func() error { return errors.New("injected rename failure") }
	t.Cleanup(func() { parentRenameHook = old })

	out := approvedEdit(t, r, editArgs("a.txt", "old", "new"))
	if !strings.Contains(out, "Error:") || !strings.Contains(out, "replace") {
		t.Fatalf("rename failure was not reported: %s", out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(data) != "old\n" {
		t.Fatalf("original bytes changed after failed rename: %q %v", data, err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".nabd-tmp-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files survived failed rename: %v %v", matches, err)
	}
}

func TestAuditDetailEscapesBidiControls(t *testing.T) {
	got := sanitizeAuditDetail("safe\u202Eevil\n")
	if strings.Contains(got, "\u202e") || !strings.Contains(got, "\\u202E") || !strings.Contains(got, "\\u000A") {
		t.Fatalf("audit detail did not escape controls: %q", got)
	}
}

func TestExecutingEventSurvivesResultLoggingGap(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "old\n", 0o600)
	r := mustRegistry(t, dir)
	defer r.Close()

	old := parentRenameHook
	parentRenameHook = func() error { return errors.New("injected failure") }
	t.Cleanup(func() { parentRenameHook = old })
	approvedEdit(t, r, editArgs("a.txt", "old", "new"))

	data, err := os.ReadFile(filepath.Join(dir, ".nabd", "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"event":"executing"`) || !strings.Contains(text, `"event":"failed"`) {
		t.Fatalf("audit log lacks pre-execution or failure record: %s", text)
	}
}
