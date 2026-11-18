package tools

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestListDirSpansMultipleBatches(t *testing.T) {
	root, reg := newTestRegistry(t)
	const extra = 500
	for i := 0; i < maxListEntries+extra; i++ {
		writeTestFile(t, filepath.Join(root, fmt.Sprintf("f%04d.txt", i)), "x")
	}

	out := runTool(t, reg, "list_dir", map[string]any{"path": "."})
	if strings.HasPrefix(out, "Error") {
		t.Fatalf("list_dir: %s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != maxListEntries+1 {
		t.Fatalf("listed %d lines, want %d", len(lines), maxListEntries+1)
	}
	if lines[0] != "F  f0000.txt" {
		t.Fatalf("first entry = %q", lines[0])
	}
	if lines[maxListEntries-1] != fmt.Sprintf("F  f%04d.txt", maxListEntries-1) {
		t.Fatalf("last shown entry = %q", lines[maxListEntries-1])
	}
	if !strings.Contains(out, fmt.Sprintf("%d more entries", extra)) {
		t.Fatalf("missing exact hidden count:\n%s", out)
	}
}
