package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runGrep(t *testing.T, reg *Registry, args string) string {
	t.Helper()
	return reg.Run(context.Background(), "grep_search", json.RawMessage(args))
}

func decodeGrep(t *testing.T, out string) grepSearchResult {
	t.Helper()
	var res grepSearchResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("grep_search output is not JSON: %v\n%s", err, out)
	}
	return res
}

func TestGrepSearchIsReadOnly(t *testing.T) {
	reg := newTestRegistryWithCleanup(t, t.TempDir())
	if reg.RequiresApproval("grep_search") {
		t.Fatal("grep_search must not require approval")
	}
	if !reg.IsReadOnly("grep_search") {
		t.Fatal("grep_search must be read-only")
	}
}

func TestGrepSearchBasicAndDeterministic(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "package a\n\nfunc Alpha() {}\n", 0o600)
	mustWrite(t, filepath.Join(dir, "b.txt"), "first\nvalue := AlphaValue\n", 0o600)
	reg := newTestRegistryWithCleanup(t, dir)

	out := runGrep(t, reg, `{"query":"Alpha"}`)
	res := decodeGrep(t, out)
	if len(res.Matches) != 2 || res.TotalMatches != 2 || res.Truncated {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Matches[0].Path != "a.go" || res.Matches[0].Line != 3 || !strings.Contains(res.Matches[0].Text, "Alpha") {
		t.Fatalf("unexpected first match: %+v", res.Matches[0])
	}
	if res.Matches[1].Path != "b.txt" || res.Matches[1].Line != 2 {
		t.Fatalf("unexpected second match: %+v", res.Matches[1])
	}
	if res.FilesScanned != 2 {
		t.Fatalf("files_scanned = %d, want 2", res.FilesScanned)
	}

	if again := runGrep(t, reg, `{"query":"Alpha"}`); again != out {
		t.Fatalf("grep_search is not deterministic:\n%s\n%s", out, again)
	}
	if _, err := json.Marshal(res.Matches); err != nil {
		t.Fatalf("matches not serializable: %v", err)
	}
}

func TestGrepSearchCaseSensitivity(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "Alpha\nalpha\n", 0o600)
	reg := newTestRegistryWithCleanup(t, dir)

	if res := decodeGrep(t, runGrep(t, reg, `{"query":"alpha"}`)); len(res.Matches) != 1 || res.Matches[0].Line != 2 {
		t.Fatalf("case-sensitive search wrong: %+v", res)
	}
	if res := decodeGrep(t, runGrep(t, reg, `{"query":"alpha","case_sensitive":false}`)); len(res.Matches) != 2 {
		t.Fatalf("case-insensitive search wrong: %+v", res)
	}
}

func TestGrepSearchRegexAndLiteral(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "func Alpha() {}\nfunc Beta() {}\na.b\n", 0o600)
	reg := newTestRegistryWithCleanup(t, dir)

	if res := decodeGrep(t, runGrep(t, reg, `{"query":"Al(pha|bet)","is_regex":true}`)); len(res.Matches) != 1 {
		t.Fatalf("regex search wrong: %+v", res)
	}
	if res := decodeGrep(t, runGrep(t, reg, `{"query":"a.b"}`)); len(res.Matches) != 1 || res.Matches[0].Line != 3 {
		t.Fatalf("literal search must not treat '.' as regex: %+v", res)
	}
	if out := runGrep(t, reg, `{"query":"(","is_regex":true}`); !strings.Contains(out, "invalid regex") {
		t.Fatalf("invalid regex must fail: %s", out)
	}
}

func TestGrepSearchGlobAndSkips(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "needle\n", 0o600)
	mustWrite(t, filepath.Join(dir, "b.txt"), "needle\n", 0o600)
	for _, sub := range []string{"node_modules", "vendor", ".git"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(dir, "node_modules", "c.js"), "needle\n", 0o600)
	mustWrite(t, filepath.Join(dir, "vendor", "d.go"), "needle\n", 0o600)
	mustWrite(t, filepath.Join(dir, ".git", "config"), "needle\n", 0o600)
	mustWrite(t, filepath.Join(dir, ".env"), "needle\n", 0o600)
	reg := newTestRegistryWithCleanup(t, dir)

	res := decodeGrep(t, runGrep(t, reg, `{"query":"needle"}`))
	if len(res.Matches) != 2 {
		t.Fatalf("skipped directories leaked into results: %+v", res)
	}
	for _, m := range res.Matches {
		if m.Path == ".env" || strings.HasPrefix(m.Path, ".git") {
			t.Fatalf("sensitive path matched: %s", m.Path)
		}
	}

	res = decodeGrep(t, runGrep(t, reg, `{"query":"needle","file_glob":"*.go"}`))
	if len(res.Matches) != 1 || res.Matches[0].Path != "a.go" {
		t.Fatalf("glob filter wrong: %+v", res)
	}
}

func TestGrepSearchRejectsSensitiveAndInvalidPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, ".env"), "secret\n", 0o600)
	mustWrite(t, filepath.Join(dir, ".git", "config"), "secret\n", 0o600)
	reg := newTestRegistryWithCleanup(t, dir)

	for _, args := range []string{
		`{"query":"secret","path":".env"}`,
		`{"query":"x","path":".git"}`,
		`{"query":"x","path":".."}`,
		`{"query":"x","path":"/etc"}`,
	} {
		if out := runGrep(t, reg, args); !strings.HasPrefix(out, "Error:") {
			t.Fatalf("%s must be rejected, got: %s", args, out)
		}
	}
}

func TestGrepSearchSkipsSymlinksAndBinaries(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "target.txt"), "needle\n", 0o600)
	mustWrite(t, filepath.Join(dir, "bin.dat"), "needle\x00hidden\n", 0o600)
	if err := os.Symlink("target.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Logf("symlinks unavailable: %v", err)
	}
	reg := newTestRegistryWithCleanup(t, dir)

	res := decodeGrep(t, runGrep(t, reg, `{"query":"needle"}`))
	if len(res.Matches) != 1 || res.Matches[0].Path != "target.txt" {
		t.Fatalf("symlink or binary file leaked: %+v", res)
	}
	if res.FilesScanned != 2 {
		t.Fatalf("files_scanned = %d, want 2 (binary is inspected then skipped)", res.FilesScanned)
	}
}

func TestGrepSearchArgumentValidation(t *testing.T) {
	reg := newTestRegistryWithCleanup(t, t.TempDir())
	longQuery := strings.Repeat("a", maxGrepQueryBytes+1)
	for _, args := range []string{
		`{}`,
		`{"query":"   "}`,
		`{"query":"` + longQuery + `"}`,
		`{"query":"x","max_results":0}`,
		`{"query":"x","max_results":101}`,
		`{"query":"x","file_glob":"["}`,
		`{"query":"x","unknown_field":true}`,
	} {
		if out := runGrep(t, reg, args); !strings.HasPrefix(out, "Error:") {
			t.Fatalf("%s must be rejected, got: %s", args, out)
		}
	}
}

func TestGrepSearchTruncatesResults(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "many.txt"), strings.Repeat("needle\n", 200), 0o600)
	reg := newTestRegistryWithCleanup(t, dir)

	res := decodeGrep(t, runGrep(t, reg, `{"query":"needle","max_results":10}`))
	if len(res.Matches) != 10 || res.TotalMatches != 200 || !res.Truncated {
		t.Fatalf("truncation wrong: len=%d total=%d truncated=%v", len(res.Matches), res.TotalMatches, res.Truncated)
	}
}

func TestGrepSearchSkipsOversizedFilesAndTrimsLines(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "big.txt"), "needle\n"+strings.Repeat("x", maxGrepFileBytes), 0o600)
	longLine := "needle " + strings.Repeat("y", 400)
	mustWrite(t, filepath.Join(dir, "long.txt"), longLine+"\n", 0o600)
	reg := newTestRegistryWithCleanup(t, dir)

	res := decodeGrep(t, runGrep(t, reg, `{"query":"needle"}`))
	if len(res.Matches) != 1 || res.Matches[0].Path != "long.txt" {
		t.Fatalf("oversized file must be skipped: %+v", res)
	}
	if len(res.Matches[0].Text) > maxGrepLineBytes || !strings.HasSuffix(res.Matches[0].Text, "...") {
		t.Fatalf("long line not trimmed: len=%d", len(res.Matches[0].Text))
	}
}

func TestGrepSearchOutputStaysWithinBudget(t *testing.T) {
	dir := t.TempDir()
	line := "needle " + strings.Repeat("z", 230)
	mustWrite(t, filepath.Join(dir, "many.txt"), strings.Repeat(line+"\n", 100), 0o600)
	reg := newTestRegistryWithCleanup(t, dir)

	out := runGrep(t, reg, `{"query":"needle","max_results":100}`)
	if len(out) > maxGrepOutputBytes {
		t.Fatalf("output exceeded budget: %d bytes", len(out))
	}
	res := decodeGrep(t, out)
	if !res.Truncated || res.TotalMatches != 100 {
		t.Fatalf("budget truncation flag wrong: %+v", res)
	}
}

func TestGrepSearchSingleFilePath(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "needle\n", 0o600)
	mustWrite(t, filepath.Join(dir, "b.txt"), "needle\n", 0o600)
	reg := newTestRegistryWithCleanup(t, dir)

	res := decodeGrep(t, runGrep(t, reg, `{"query":"needle","path":"b.txt"}`))
	if len(res.Matches) != 1 || res.Matches[0].Path != "b.txt" {
		t.Fatalf("single-file search wrong: %+v", res)
	}
}
