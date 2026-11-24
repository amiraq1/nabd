package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationUndoRejectsChangeBeforeRename(t *testing.T) {
	dir, r := newTestRegistry(t)
	path := filepath.Join(dir, "target.txt")
	writeTestFile(t, path, "before")
	out := runToolApproved(t, r, "edit_file", map[string]any{"path": "target.txt", "old_text": "before", "new_text": "after"})
	if !strings.HasPrefix(out, "Successfully updated") {
		t.Fatalf("setup edit failed: %s", out)
	}
	called := false
	editBeforeRenameHook = func() {
		called = true
		if err := os.WriteFile(path, []byte("concurrent change"), 0600); err != nil {
			t.Errorf("fixture write: %v", err)
		}
	}
	defer func() { editBeforeRenameHook = nil }()
	if _, err := r.Undo("target.txt"); err == nil {
		t.Error("undo accepted a file changed immediately before rename")
	}
	if !called {
		t.Error("undo did not perform pre-rename recheck")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "concurrent change" {
		t.Error("undo overwrote the concurrent change")
	}
}
