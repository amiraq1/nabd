package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditPreservesMixedBytesAndApprovedOutputHash(t *testing.T) {
	root, r := newTestRegistry(t)
	before := "a\r\nb\nc\r\n"
	writeTestFile(t, filepath.Join(root, "mixed.txt"), before)
	raw, _ := json.Marshal(editParams{Path: "mixed.txt", OldText: "b", NewText: "B"})
	p, err := r.PreviewApproval("edit_file", raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.State.OutputHash != hashBytes([]byte("a\r\nB\nc\r\n")) {
		t.Fatal("preview output digest incorrect")
	}
	altered := p.State
	altered.OutputHash = hashBytes([]byte("different"))
	approval, err := r.ApprovePreview("edit_file", raw, altered)
	if err != nil {
		t.Fatal(err)
	}
	if out := r.RunApproved(context.Background(), "edit_file", raw, approval); !strings.Contains(out, "output differs") {
		t.Fatalf("tampered digest accepted: %s", out)
	}
	actual, _ := os.ReadFile(filepath.Join(root, "mixed.txt"))
	if string(actual) != before {
		t.Fatal("file changed after denial")
	}
	approval, err = r.ApprovePreview("edit_file", raw, p.State)
	if err != nil {
		t.Fatal(err)
	}
	if out := r.RunApproved(context.Background(), "edit_file", raw, approval); !strings.HasPrefix(out, "Successfully") {
		t.Fatal(out)
	}
	actual, _ = os.ReadFile(filepath.Join(root, "mixed.txt"))
	if string(actual) != "a\r\nB\nc\r\n" {
		t.Fatalf("unrelated bytes changed: %q", actual)
	}
}
