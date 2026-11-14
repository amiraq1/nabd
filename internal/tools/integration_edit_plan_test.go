package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationPreviewAndApplyRejectSameNoops(t *testing.T) {
	tests := []struct{ name, path, content, old, new, reason string }{
		{"identical", "a.txt", "alpha\nbeta\n", "alpha", "alpha", "old_text and new_text are identical"},
		{"line endings normalize", "b.txt", "alpha\nbeta\n", "alpha\r\n", "alpha\n", "identical after line-ending normalization"},
		{"formatting removes change", "c.go", "package p\n\nfunc f() int {\n\treturn 1\n}\n", "return 1", "return  1", "edit would not change"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root, r := newTestRegistry(t)
			writeTestFile(t, filepath.Join(root, tc.path), tc.content)
			raw, err := json.Marshal(map[string]string{"path": tc.path, "old_text": tc.old, "new_text": tc.new})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.PreviewApproval("edit_file", raw); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Error("preview did not reject no-op with expected reason")
			}
			if _, err := r.handlers["edit_file"].Execute(context.Background(), raw); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Error("execution did not reject no-op with expected reason")
			}
			data, err := os.ReadFile(filepath.Join(root, tc.path))
			if err != nil || string(data) != tc.content {
				t.Fatal("no-op changed file")
			}
		})
	}
}

func TestIntegrationEditUsesPreviewedTransformation(t *testing.T) {
	root, r := newTestRegistry(t)
	before := "package p\n\nfunc f() int {\n\treturn 1\n}\n"
	writeTestFile(t, filepath.Join(root, "a.go"), before)
	args := map[string]any{"path": "a.go", "old_text": "return 1", "new_text": "return  2"}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := r.PreviewApproval("edit_file", raw)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := applyEdit([]byte(before), "a.go", "a.go", "return 1", "return  2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.Display, "+\treturn 2") {
		t.Fatal("preview omitted formatted output")
	}
	out := runToolApproved(t, r, "edit_file", args)
	if !strings.HasPrefix(out, "Successfully updated") {
		t.Fatalf("edit failed: %s", out)
	}
	actual, err := os.ReadFile(filepath.Join(root, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(expected) {
		t.Fatal("written bytes differ from preview transformation")
	}
}
