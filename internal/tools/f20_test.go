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

func f20State(t *testing.T, r *Registry, relPath string) ApprovalState {
	t.Helper()
	safePath, err := r.resolveSafePath(relPath)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(safePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	return ApprovalState{Path: safePath, Exists: true, Hash: hex.EncodeToString(sum[:])}
}

func TestF20RunApprovedValidatesEditWithoutPreview(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		content string
		args    string
	}{
		{
			name:    "missing_old_text",
			file:    "a.txt",
			content: "old\n",
			args:    `{"path":"a.txt","old_text":"absent","new_text":"new"}`,
		},
		{
			name:    "duplicate_old_text",
			file:    "a.txt",
			content: "x\nx\n",
			args:    `{"path":"a.txt","old_text":"x","new_text":"y"}`,
		},
		{
			name:    "invalid_go",
			file:    "bad.go",
			content: "package bad\n",
			args:    `{"path":"bad.go","old_text":"package bad","new_text":"package bad\nfunc {"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, tc.file)
			mustWrite(t, target, tc.content, 0600)
			r := mustRegistry(t, dir)
			state := f20State(t, r, tc.file)

			out := r.RunApproved(context.Background(), "edit_file", json.RawMessage(tc.args), state)
			if !strings.Contains(out, "Error") {
				t.Fatalf("expected a rejection, got %q", out)
			}
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.content {
				t.Fatalf("file changed: %q", got)
			}
			matches, err := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 0 {
				t.Fatalf("temporary files left behind: %v", matches)
			}
		})
	}
}
