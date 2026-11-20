package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func newPR7TestRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	dir := t.TempDir()
	return mustRegistry(t, dir), dir
}

func mustRegistry(t *testing.T, dir string) *Registry {
	t.Helper()
	r, err := NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestResolveSafePathRejectsEscapes(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	if err := os.MkdirAll(filepath.Join(work, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	r := mustRegistry(t, work)

	outside := filepath.Join(base, "outside.txt")
	mustWrite(t, outside, "secret", 0600)
	if err := os.Symlink(outside, filepath.Join(work, "link.txt")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(work, "ok.txt"), "inside", 0600)

	escapes := []string{
		"../outside.txt",
		"sub/../../outside.txt",
		"link.txt",
		outside,
		"..",
	}
	for _, p := range escapes {
		if _, err := r.resolveSafePath(p); err == nil {
			t.Errorf("resolveSafePath(%q) allowed an escape", p)
		}
	}

	if _, err := r.resolveSafePath("ok.txt"); err != nil {
		t.Errorf("resolveSafePath(%q) rejected a legal path: %v", "ok.txt", err)
	}
}

func TestReadFileJailBlocksPrefixSibling(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(base, "work-evil")
	if err := os.MkdirAll(sibling, 0755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sibling, "secret.txt"), "SIBLING-SECRET", 0600)

	r := mustRegistry(t, work)
	out := r.Run(context.Background(), "read_file", json.RawMessage(`{"path":"../work-evil/secret.txt"}`))
	if strings.Contains(out, "SIBLING-SECRET") {
		t.Fatalf("read_file escaped to prefix sibling: %q", out)
	}
}

func TestEditFileRequiresApprovalWhenNoApprover(t *testing.T) {
	r, dir := newPR7TestRegistry(t)
	p := filepath.Join(dir, "a.txt")
	mustWrite(t, p, "old\n", 0644)

	out := r.Run(context.Background(), "edit_file", json.RawMessage(`{"path":"a.txt","old_text":"old","new_text":"new"}`))
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old\n" {
		t.Fatalf("edit_file executed without an approver: file=%q tool_output=%q", got, out)
	}
}

func runEditApproved(t *testing.T, r *Registry, args json.RawMessage) string {
	t.Helper()
	preview, err := r.PreviewApproval("edit_file", args)
	if err != nil {
		t.Fatalf("PreviewApproval: %v", err)
	}
	return legacyApproved(t, r, context.Background(), "edit_file", args, preview.State)
}

func TestEditFilePreservesCRLFInGoFile(t *testing.T) {
	dir := t.TempDir()
	r := mustRegistry(t, dir)
	p := filepath.Join(dir, "x.go")
	mustWrite(t, p, "package x\r\n\r\nfunc A() {}\r\n", 0644)

	out := runEditApproved(t, r, json.RawMessage(`{"path":"x.go","old_text":"func A() {}","new_text":"func A() { _ = 1 }"}`))
	if !strings.HasPrefix(out, "Successfully") {
		t.Fatalf("edit failed: %q", out)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "\r\n") {
		t.Fatalf("CRLF was converted to LF: %q", got)
	}
	withoutCRLF := strings.ReplaceAll(string(got), "\r\n", "")
	if strings.Contains(withoutCRLF, "\n") {
		t.Fatalf("mixed line endings after edit: %q", got)
	}
}

func TestEditFileUsesAtomicRename(t *testing.T) {
	dir := t.TempDir()
	r := mustRegistry(t, dir)
	p := filepath.Join(dir, "a.txt")
	mustWrite(t, p, "old\n", 0600)

	f1, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	before, err := f1.Stat()
	_ = f1.Close()
	if err != nil {
		t.Fatal(err)
	}
	out := runEditApproved(t, r, json.RawMessage(`{"path":"a.txt","old_text":"old","new_text":"new"}`))
	if !strings.HasPrefix(out, "Successfully") {
		t.Fatalf("edit failed: %q", out)
	}
	f2, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	after, err := f2.Stat()
	_ = f2.Close()
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("edit_file wrote in place; expected atomic temp+rename")
	}
	if runtime.GOOS != "windows" && after.Mode().Perm() != 0600 {
		t.Fatalf("mode changed: got %o want 0600", after.Mode().Perm())
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new\n" {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestEditFileRejectsAmbiguousAndSyntaxErrors(t *testing.T) {
	dir := t.TempDir()
	r := mustRegistry(t, dir)

	p := filepath.Join(dir, "dup.txt")
	mustWrite(t, p, "x\nx\n", 0644)
	if _, err := r.PreviewApproval("edit_file", json.RawMessage(`{"path":"dup.txt","old_text":"x","new_text":"y"}`)); err == nil {
		t.Fatal("ambiguous match was not rejected")
	}

	g := filepath.Join(dir, "bad.go")
	mustWrite(t, g, "package bad\n", 0644)
	_, err := r.PreviewApproval("edit_file", json.RawMessage(`{"path":"bad.go","old_text":"package bad","new_text":"package bad\nfunc {"}`))
	if err == nil || !strings.Contains(err.Error(), "syntax") {
		t.Fatalf("invalid Go was not rejected: %v", err)
	}
	got, err := os.ReadFile(g)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "package bad\n" {
		t.Fatalf("invalid edit was persisted: %q", got)
	}
}

func fakeGo(t *testing.T, script string) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not found: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "go")
	if err := os.WriteFile(path, []byte("#!"+sh+"\n"+script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestGoVerifyReceivesMemoryLimits(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "env.txt")
	fakeGo(t, `test "$GOMEMLIMIT" = "512MiB" && test "$GOGC" = "50" && printf ok > `+strconv.Quote(outFile))

	r := mustRegistry(t, dir)
	out := legacyApproved(t, r, context.Background(), "go_verify", json.RawMessage(`{"pkg":"./..."}`), ApprovalState{})
	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("probe not written (tool output %q): %v", out, err)
	}
	if string(got) != "ok" {
		t.Fatal("memory-limit environment check failed")
	}
}

func TestGoVerifyRejectsFlagLikePkg(t *testing.T) {
	r := mustRegistry(t, t.TempDir())
	out := legacyApproved(t, r, context.Background(), "go_verify", json.RawMessage(`{"pkg":"-exec=sh"}`), ApprovalState{})
	if !strings.Contains(out, "flags are not allowed") {
		t.Fatalf("flag injection was not rejected: %q", out)
	}
}

func TestGoVerifyRejectsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module probe\n\ngo 1.22\n", 0644)
	mustWrite(t, filepath.Join(dir, "probe_test.go"), "package probe\n\nimport \"testing\"\n\nfunc TestP(t *testing.T) {}\n", 0644)

	r := mustRegistry(t, dir)
	out := legacyApproved(t, r, context.Background(), "go_verify", json.RawMessage(`{"pkg":`), ApprovalState{})
	if !strings.Contains(out, "Error") {
		t.Fatalf("malformed arguments ran the tool instead of failing: %q", out)
	}
}

func TestGoVerifyTimeoutKillsChildProcesses(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "alive.txt")
	fakeGo(t, `( sleep 2; printf alive > `+strconv.Quote(marker)+` ) &`+"\nwait")

	r := mustRegistry(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	out := legacyApproved(t, r, ctx, "go_verify", json.RawMessage(`{"pkg":"./..."}`), ApprovalState{})
	if !strings.Contains(out, "timeout") {
		t.Fatalf("expected a timeout result, got %q", out)
	}

	time.Sleep(3 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("grandchild survived the go_verify timeout and kept running")
	}
}

func TestTruncateOutputKeepsValidUTF8(t *testing.T) {
	s := strings.Repeat("مرحبا", 2000)
	got := truncateOutput(s)
	if !utf8.ValidString(got) {
		t.Fatal("truncateOutput produced invalid UTF-8")
	}
	if !strings.Contains(got, "output truncated") {
		t.Fatalf("expected truncation marker, got %d bytes", len(got))
	}
}
