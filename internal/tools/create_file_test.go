package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/amiraq1/nabd/internal/audit"
)

func createFileArgs(t *testing.T, path string, content *string) json.RawMessage {
	t.Helper()
	payload := map[string]any{"path": path}
	if content != nil {
		payload["content"] = *content
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func runApprovedCreate(t *testing.T, reg *Registry, path, content string) string {
	t.Helper()
	raw := createFileArgs(t, path, &content)
	preview, err := reg.PreviewApproval("create_file", raw)
	if err != nil {
		return "Error: " + err.Error()
	}
	approval, err := reg.ApprovePreview("create_file", raw, preview.State)
	if err != nil {
		return "Error: " + err.Error()
	}
	return reg.RunApproved(context.Background(), "create_file", raw, approval)
}

// 1. Original bug regression: edit_file on a missing file must NEVER report
// "file changed since approval"; it must fail early with "file does not exist"
// and direct the caller to create_file.
func TestCreateBugRegressionEditMissingFile(t *testing.T) {
	_, reg := newTestRegistry(t)
	raw := json.RawMessage(`{"path":"go.mod","old_text":"module demo","new_text":"module demo\n\ngo 1.26\n"}`)

	if _, err := reg.PreviewApproval("edit_file", raw); err == nil {
		t.Fatal("PreviewApproval(edit_file) on missing file unexpectedly succeeded; want early error")
	} else {
		if strings.Contains(err.Error(), "file changed since approval") {
			t.Fatalf("misleading error in PreviewApproval: %v", err)
		}
		if !strings.Contains(err.Error(), "file does not exist") || !strings.Contains(err.Error(), "create_file") {
			t.Fatalf("PreviewApproval error = %v, want file does not exist + use create_file", err)
		}
	}

	approval, err := reg.Approve("edit_file", raw)
	if err != nil {
		t.Fatalf("Approve(edit_file): %v", err)
	}
	out := reg.RunApproved(context.Background(), "edit_file", raw, approval)
	if strings.Contains(out, "file changed since approval") {
		t.Fatalf("RunApproved produced misleading error: %s", out)
	}
	if !strings.Contains(out, "file does not exist") || !strings.Contains(out, "create_file") {
		t.Fatalf("RunApproved = %q, want file does not exist + use create_file", out)
	}

	// Also verify that if a file existed at preview and is deleted before execution,
	// edit_file does not report "file changed since approval".
	root, reg2 := newTestRegistry(t)
	delPath := filepath.Join(root, "deleted.txt")
	mustWrite(t, delPath, "old\n", 0o600)
	delRaw := json.RawMessage(`{"path":"deleted.txt","old_text":"old","new_text":"new"}`)
	p, err := reg2.PreviewApproval("edit_file", delRaw)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := reg2.ApprovePreview("edit_file", delRaw, p.State)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(delPath); err != nil {
		t.Fatal(err)
	}
	delOut := reg2.RunApproved(context.Background(), "edit_file", delRaw, tok)
	if strings.Contains(delOut, "file changed since approval") {
		t.Fatalf("deleted file produced misleading error: %s", delOut)
	}
	if !strings.Contains(delOut, "no longer exists") {
		t.Fatalf("deleted file error = %q, want no longer exists", delOut)
	}
}

// 2. Successful creation in an empty directory: go.mod, main.go, pkg/sub/a.go
// via preview -> approve -> execute, including empty content and audit events.
func TestCreateFileInEmptyWorkspaceAndSubdirs(t *testing.T) {
	root, reg := newTestRegistry(t)

	files := []struct {
		path    string
		content string
	}{
		{"go.mod", "module example.com/demo\n\ngo 1.26\n"},
		{"main.go", "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"ok\")\n}\n"},
		{"pkg/sub/a.go", "package sub\n\nfunc Hello() string { return \"hi\" }\n"},
		{".gitkeep", ""},
	}

	for _, tc := range files {
		raw := createFileArgs(t, tc.path, &tc.content)
		preview, err := reg.PreviewApproval("create_file", raw)
		if err != nil {
			t.Fatalf("PreviewApproval(%s): %v", tc.path, err)
		}
		if preview.State.Exists {
			t.Fatalf("%s: expected Exists=false", tc.path)
		}
		if preview.State.OutputHash != hashBytes([]byte(tc.content)) {
			t.Fatalf("%s: OutputHash mismatch", tc.path)
		}
		if tc.content == "" && !strings.Contains(preview.Display, "empty file, 0 bytes") {
			t.Fatalf("empty file preview missing notice: %q", preview.Display)
		}
		approval, err := reg.ApprovePreview("create_file", raw, preview.State)
		if err != nil {
			t.Fatalf("ApprovePreview(%s): %v", tc.path, err)
		}
		out := reg.RunApproved(context.Background(), "create_file", raw, approval)
		if !strings.HasPrefix(out, "Successfully created") {
			t.Fatalf("RunApproved(%s) = %q", tc.path, out)
		}

		diskPath := filepath.Join(root, filepath.FromSlash(tc.path))
		got, err := os.ReadFile(diskPath)
		if err != nil {
			t.Fatalf("read %s: %v", tc.path, err)
		}
		if string(got) != tc.content {
			t.Fatalf("%s content = %q, want %q", tc.path, got, tc.content)
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(diskPath)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o111 != 0 {
				t.Fatalf("%s should not have execute bits, got %o", tc.path, info.Mode().Perm())
			}
		}
	}

	// Verify audit.log for go.mod has requested, approved, executing, applied
	// and never leaks file content.
	goModContent := files[0].content
	goModRaw := createFileArgs(t, "go.mod", &goModContent)
	goModArgsHash := hashBytes(goModRaw)
	events, rawLog := readAuditLog(t, root)
	var goModEvents []audit.Event
	for _, ev := range events {
		if ev.Tool == "create_file" && ev.ArgsSHA256 == goModArgsHash {
			goModEvents = append(goModEvents, ev)
		}
	}
	wantSeq := []string{"requested", "approved", "executing", "applied"}
	if len(goModEvents) != len(wantSeq) {
		t.Fatalf("go.mod audit events = %d (%+v), want %d", len(goModEvents), goModEvents, len(wantSeq))
	}
	for i, w := range wantSeq {
		if goModEvents[i].Event != w {
			t.Errorf("go.mod event[%d] = %q, want %q", i, goModEvents[i].Event, w)
		}
	}
	if strings.Contains(rawLog, "example.com/demo") || strings.Contains(rawLog, "fmt.Println") {
		t.Fatalf("audit log leaked file content:\n%s", rawLog)
	}
	if !strings.HasPrefix(goModEvents[0].Detail, "go.mod ") ||
		!strings.Contains(goModEvents[0].Detail, "sha256=") ||
		!strings.Contains(goModEvents[0].Detail, "size=") {
		t.Fatalf("audit detail missing path/sha256/size: %q", goModEvents[0].Detail)
	}
	if goModEvents[2].Detail != goModEvents[0].Detail {
		t.Fatalf("executing detail = %q, want %q", goModEvents[2].Detail, goModEvents[0].Detail)
	}
	if goModEvents[3].ResultSHA256 == "" {
		t.Fatalf("applied event missing ResultSHA256")
	}
}

// 3. TOCTOU: file appears on disk between preview and execution (and right before publish)
// -> must fail closed without touching existing file or its permissions.
func TestCreateFileTOCTOUAppearsBetweenPreviewAndExec(t *testing.T) {
	t.Run("appears_after_approval", func(t *testing.T) {
		root, reg := newTestRegistry(t)
		content := "new content\n"
		raw := createFileArgs(t, "race.txt", &content)
		preview, err := reg.PreviewApproval("create_file", raw)
		if err != nil {
			t.Fatal(err)
		}
		approval, err := reg.ApprovePreview("create_file", raw, preview.State)
		if err != nil {
			t.Fatal(err)
		}

		target := filepath.Join(root, "race.txt")
		mustWrite(t, target, "existing\n", 0o600)

		out := reg.RunApproved(context.Background(), "create_file", raw, approval)
		if !strings.Contains(out, "file already exists") {
			t.Fatalf("expected file already exists, got %q", out)
		}
		got, err := os.ReadFile(target)
		if err != nil || string(got) != "existing\n" {
			t.Fatalf("existing file was modified: %q %v", got, err)
		}
		if runtime.GOOS != "windows" {
			info, _ := os.Stat(target)
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("existing file permissions altered: %o", info.Mode().Perm())
			}
		}
	})

	t.Run("appears_before_publish_hook", func(t *testing.T) {
		root, reg := newTestRegistry(t)
		content := "new content\n"
		raw := createFileArgs(t, "hookrace.txt", &content)
		preview, err := reg.PreviewApproval("create_file", raw)
		if err != nil {
			t.Fatal(err)
		}
		approval, err := reg.ApprovePreview("create_file", raw, preview.State)
		if err != nil {
			t.Fatal(err)
		}

		target := filepath.Join(root, "hookrace.txt")
		editBeforeRenameHook = func() {
			if err := os.WriteFile(target, []byte("raced\n"), 0o600); err != nil {
				t.Errorf("hook write: %v", err)
			}
		}
		defer func() { editBeforeRenameHook = nil }()

		out := reg.RunApproved(context.Background(), "create_file", raw, approval)
		if !strings.Contains(out, "file already exists") {
			t.Fatalf("expected file already exists on pre-publish race, got %q", out)
		}
		got, err := os.ReadFile(target)
		if err != nil || string(got) != "raced\n" {
			t.Fatalf("raced file was overwritten: %q %v", got, err)
		}
		matches, _ := filepath.Glob(filepath.Join(root, ".nabd-tmp-*"))
		if len(matches) != 0 {
			t.Fatalf("temp files survived failed publish: %v", matches)
		}
	})
}

// 4. Content & path binding: tampering with OutputHash or Path or arguments
// between approval and execution fails closed.
func TestCreateFileContentAndPathBinding(t *testing.T) {
	root, reg := newTestRegistry(t)
	content := "approved bytes\n"
	raw := createFileArgs(t, "bound.txt", &content)
	preview, err := reg.PreviewApproval("create_file", raw)
	if err != nil {
		t.Fatal(err)
	}

	tamperedState := preview.State
	tamperedState.OutputHash = hashBytes([]byte("different bytes\n"))
	approval, err := reg.ApprovePreview("create_file", raw, tamperedState)
	if err != nil {
		t.Fatal(err)
	}
	if out := reg.RunApproved(context.Background(), "create_file", raw, approval); !strings.Contains(out, "output differs") {
		t.Fatalf("tampered OutputHash accepted: %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "bound.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bound.txt should not exist after OutputHash mismatch")
	}

	// Arguments mismatch after approval
	approval2, err := reg.ApprovePreview("create_file", raw, preview.State)
	if err != nil {
		t.Fatal(err)
	}
	otherContent := "tampered args\n"
	otherRaw := createFileArgs(t, "bound.txt", &otherContent)
	if out := reg.RunApproved(context.Background(), "create_file", otherRaw, approval2); !strings.Contains(out, "invalid or expired") {
		t.Fatalf("tampered args accepted: %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "bound.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bound.txt should not exist after args mismatch")
	}
}

// 5. Security rejections: sensitive paths, traversal, absolute paths,
// symlinked parent, dangling target symlink, regular file as parent, NUL, depth/length.
func TestCreateFileSecurityRejections(t *testing.T) {
	root, reg := newTestRegistry(t)
	outside := t.TempDir()

	// Regular file as parent component
	writeTestFile(t, filepath.Join(root, "fileparent"), "not a dir\n")
	// Real .git directory to test symlink-to-.git traversal
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		path  string
		setup func(t *testing.T)
	}{
		{name: "dot_git", path: ".git/hook.sh"},
		{name: "dot_nabd", path: ".nabd/evil.txt"},
		{name: "dot_env", path: ".env"},
		{name: "dot_env_nested", path: "sub/.ENV.local"},
		{name: "traversal", path: "../escape.txt"},
		{name: "absolute", path: filepath.Join(outside, "abs.txt")},
		{name: "nul_byte", path: "bad\x00.txt"},
		{name: "regular_file_parent", path: "fileparent/sub/a.txt"},
		{name: "too_deep", path: strings.Repeat("d/", 16) + "a.txt"},
		{name: "too_long", path: strings.Repeat("a", 200) + "/" + strings.Repeat("b", 200) + "/" + strings.Repeat("c", 120) + ".txt"},
		{
			name: "symlink_parent_outside",
			path: "linkdir/sub/a.txt",
			setup: func(t *testing.T) {
				if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
					t.Skipf("symlink unsupported: %v", err)
				}
			},
		},
		{
			name: "symlink_parent_git",
			path: "gitlink/sub/config",
			setup: func(t *testing.T) {
				if err := os.Symlink(".git", filepath.Join(root, "gitlink")); err != nil {
					t.Skipf("symlink unsupported: %v", err)
				}
			},
		},
		{
			name: "dangling_target_symlink",
			path: "dangling.txt",
			setup: func(t *testing.T) {
				if err := os.Symlink("does_not_exist_target", filepath.Join(root, "dangling.txt")); err != nil {
					t.Skipf("symlink unsupported: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != nil {
				tc.setup(t)
			}
			content := "payload\n"
			raw := createFileArgs(t, tc.path, &content)
			if _, err := reg.PreviewApproval("create_file", raw); err == nil {
				t.Fatalf("PreviewApproval(%q) succeeded, want rejection", tc.path)
			}
			// Direct Approve -> RunApproved path must also reject without creating anything.
			approval, err := reg.Approve("create_file", raw)
			if err != nil {
				t.Fatalf("Approve(%q): %v", tc.path, err)
			}
			out := reg.RunApproved(context.Background(), "create_file", raw, approval)
			if !strings.HasPrefix(out, "Error:") {
				t.Fatalf("RunApproved(%q) = %q, want Error:", tc.path, out)
			}
		})
	}

	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside directory modified: %v %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "sub")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created directory inside .git via symlink")
	}
	if _, err := os.Stat(filepath.Join(root, "does_not_exist_target")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("followed dangling symlink target")
	}
}

// 6. Existing file rejected cleanly.
func TestCreateFileRejectsExistingFile(t *testing.T) {
	root, reg := newTestRegistry(t)
	target := filepath.Join(root, "exists.txt")
	mustWrite(t, target, "original\n", 0o600)

	content := "overwrite\n"
	raw := createFileArgs(t, "exists.txt", &content)
	if _, err := reg.PreviewApproval("create_file", raw); err == nil || !strings.Contains(err.Error(), "file already exists") {
		t.Fatalf("PreviewApproval on existing file = %v, want file already exists", err)
	}
	out := runToolApproved(t, reg, "create_file", map[string]any{"path": "exists.txt", "content": content})
	if !strings.Contains(out, "file already exists") || !strings.Contains(out, "edit_file") {
		t.Fatalf("RunApproved on existing file = %q, want file already exists + edit_file", out)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "original\n" {
		t.Fatalf("existing file modified: %q", got)
	}
}

// 7. Invalid Go syntax rejected without leaving files or directories on disk.
func TestCreateFileGoSyntaxValidation(t *testing.T) {
	root, reg := newTestRegistry(t)
	badGo := "package bad\n\nfunc broken( {\n"
	raw := createFileArgs(t, "pkg/bad/broken.go", &badGo)

	if _, err := reg.PreviewApproval("create_file", raw); err == nil || !strings.Contains(err.Error(), "syntax") {
		t.Fatalf("PreviewApproval allowed invalid Go: %v", err)
	}
	approval, err := reg.Approve("create_file", raw)
	if err != nil {
		t.Fatal(err)
	}
	out := reg.RunApproved(context.Background(), "create_file", raw, approval)
	if !strings.Contains(out, "syntax") {
		t.Fatalf("RunApproved allowed invalid Go: %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "pkg")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid Go create left parent directory behind")
	}
}

// 8. Concurrent create_file calls for the same path: one succeeds, one fails closed.
func TestCreateFileConcurrentSamePath(t *testing.T) {
	root, reg := newTestRegistry(t)

	c1 := "first\n"
	c2 := "second\n"
	raw1 := createFileArgs(t, "concurrent/same.txt", &c1)
	raw2 := createFileArgs(t, "concurrent/same.txt", &c2)

	p1, err := reg.PreviewApproval("create_file", raw1)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := reg.PreviewApproval("create_file", raw2)
	if err != nil {
		t.Fatal(err)
	}
	a1, err := reg.ApprovePreview("create_file", raw1, p1.State)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := reg.ApprovePreview("create_file", raw2, p2.State)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make([]string, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0] = reg.RunApproved(context.Background(), "create_file", raw1, a1)
	}()
	go func() {
		defer wg.Done()
		results[1] = reg.RunApproved(context.Background(), "create_file", raw2, a2)
	}()
	wg.Wait()

	successes := 0
	var winner string
	for i, res := range results {
		if strings.HasPrefix(res, "Successfully created") {
			successes++
			if i == 0 {
				winner = c1
			} else {
				winner = c2
			}
		} else if !strings.Contains(res, "file already exists") && !strings.Contains(res, "locked by another process") {
			t.Fatalf("loser returned unexpected error: %q", res)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly 1 success, got %d (%v)", successes, results)
	}
	got, err := os.ReadFile(filepath.Join(root, "concurrent", "same.txt"))
	if err != nil || string(got) != winner {
		t.Fatalf("winning content corrupted: got=%q want=%q err=%v", got, winner, err)
	}
}

// 9. Rollback on failure after parent creation removes ONLY newly created parents.
func TestCreateFileRollbackCreatedParentsOnFailure(t *testing.T) {
	root, reg := newTestRegistry(t)
	existingDir := filepath.Join(root, "existing")
	if err := os.MkdirAll(existingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(existingDir, "keep.txt"), "keep\n", 0o644)

	hookCalls := 0
	old := parentRenameHook
	parentRenameHook = func() error {
		hookCalls++
		return errors.New("injected publish failure")
	}
	t.Cleanup(func() { parentRenameHook = old })

	// Case A: all parents newly created -> all removed
	outA := runApprovedCreate(t, reg, "fresh/nested/a.txt", "hello\n")
	if !strings.Contains(outA, "injected publish failure") {
		t.Fatalf("expected injected failure, got %q", outA)
	}
	if _, err := os.Stat(filepath.Join(root, "fresh")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan directory 'fresh' survived failure")
	}

	// Case B: 'existing' already existed, 'existing/sub' was created -> only 'sub' removed
	outB := runApprovedCreate(t, reg, "existing/sub/b.txt", "hello\n")
	if !strings.Contains(outB, "injected publish failure") {
		t.Fatalf("expected injected failure, got %q", outB)
	}
	if hookCalls != 2 {
		t.Fatalf("expected parentRenameHook to run twice after parent creation, got %d", hookCalls)
	}
	if _, err := os.Stat(filepath.Join(existingDir, "sub")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan subdirectory 'existing/sub' survived failure")
	}
	if _, err := os.Stat(filepath.Join(existingDir, "keep.txt")); err != nil {
		t.Fatalf("pre-existing directory/file was harmed: %v", err)
	}
}

// Strict argument validation, Run fail-closed without approver, and fallback
// behavior when exclusive publish primitives are unsupported vs EEXIST.
func TestCreateFileValidationAndFailClosed(t *testing.T) {
	root, reg := newTestRegistry(t)

	if out := reg.Run(context.Background(), "create_file", json.RawMessage(`{"path":"a.txt","content":"x"}`)); !strings.Contains(out, "requires approval") {
		t.Fatalf("Run without approver did not fail closed: %q", out)
	}
	if _, err := reg.PreviewApproval("create_file", json.RawMessage(`{"path":"a.txt"}`)); err == nil || !strings.Contains(err.Error(), "content") {
		t.Fatalf("missing content field was not rejected: %v", err)
	}
	if _, err := reg.PreviewApproval("create_file", json.RawMessage(`{"path":"a.txt","content":"x","extra":1}`)); err == nil {
		t.Fatalf("unknown JSON field was not rejected")
	}

	// Unsupported exclusive primitive (e.g. FAT32/FUSE) falls back to Lstat+Rename cleanly...
	oldHook := publishNewFileHook
	t.Cleanup(func() { publishNewFileHook = oldHook })
	publishNewFileHook = func(*os.Root, *os.File, string, string) error {
		return errors.ErrUnsupported
	}
	if out := runApprovedCreate(t, reg, "fallback.txt", "ok\n"); !strings.HasPrefix(out, "Successfully created") {
		t.Fatalf("fallback publish failed: %q", out)
	}
	if got, err := os.ReadFile(filepath.Join(root, "fallback.txt")); err != nil || string(got) != "ok\n" {
		t.Fatalf("fallback file mismatch: %q %v", got, err)
	}

	// ...while ErrExist ALWAYS fails closed and never falls back to Rename.
	target := filepath.Join(root, "raced.txt")
	publishNewFileHook = func(*os.Root, *os.File, string, string) error {
		_ = os.WriteFile(target, []byte("winner\n"), 0o600)
		return os.ErrExist
	}
	out := runApprovedCreate(t, reg, "raced.txt", "loser\n")
	if !strings.Contains(out, "file already exists") {
		t.Fatalf("ErrExist did not fail closed: %q", out)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "winner\n" {
		t.Fatalf("ErrExist overwrote winner: %q %v", got, err)
	}
}
