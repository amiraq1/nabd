package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewApprovalShowsFinalNewlineChange(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{{"add", "a", "a\n"}, {"remove", "a\n", "a"}} {
		t.Run(tc.name, func(t *testing.T) {
			root, r := newTestRegistry(t)
			writeTestFile(t, filepath.Join(root, "f.txt"), tc.before)
			raw, _ := json.Marshal(editParams{Path: "f.txt", OldText: tc.before, NewText: tc.after})
			p, err := r.PreviewApproval("edit_file", raw)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(p.Display, "-a [EOL=") || !strings.Contains(p.Display, "+a [EOL=") || !strings.Contains(p.Display, `\ No newline at end of file`) {
				t.Fatalf("newline change hidden: %q", p.Display)
			}
			approval, err := r.ApprovePreview("edit_file", raw, p.State)
			if err != nil {
				t.Fatal(err)
			}
			out := r.RunApproved(context.Background(), "edit_file", raw, approval)
			if !strings.HasPrefix(out, "Successfully updated") {
				t.Fatalf("edit failed: %s", out)
			}
			actual, err := os.ReadFile(filepath.Join(root, "f.txt"))
			if err != nil || string(actual) != tc.after {
				t.Fatal("unexpected output bytes")
			}
		})
	}
}
func TestReviewApprovalPreservesMixedEOL(t *testing.T) {
	root, r := newTestRegistry(t)
	before := "a\r\nb\nc\n"
	writeTestFile(t, filepath.Join(root, "f.txt"), before)
	raw, _ := json.Marshal(editParams{Path: "f.txt", OldText: "b", NewText: "B"})
	p, err := r.PreviewApproval("edit_file", raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"-b [EOL=LF]", "+B [EOL=LF]", " c [EOL=LF]"} {
		if !strings.Contains(p.Display, part) {
			t.Errorf("missing EOL change %q in %q", part, p.Display)
		}
	}
	approval, err := r.ApprovePreview("edit_file", raw, p.State)
	if err != nil {
		t.Fatal(err)
	}
	out := r.RunApproved(context.Background(), "edit_file", raw, approval)
	if !strings.HasPrefix(out, "Successfully updated") {
		t.Fatalf("edit failed: %s", out)
	}
	actual, err := os.ReadFile(filepath.Join(root, "f.txt"))
	if err != nil || string(actual) != "a\r\nB\nc\n" {
		t.Fatal("unexpected output bytes")
	}
}
