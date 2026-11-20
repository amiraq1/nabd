package tools

import (
	"context"
	"encoding/json"
	shadowstore "github.com/amiraq1/nabd/internal/shadow"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// 1. Path traversal via ../ and nested variants
func TestSecurity_PathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	r := mustRegistry(t, dir)

	// Create a secret file outside the workspace root
	parent := filepath.Dir(dir)
	secretFile := filepath.Join(parent, "host_secret.txt")
	_ = os.WriteFile(secretFile, []byte("SUPER_SECRET_DATA"), 0600)
	defer os.Remove(secretFile)

	traversalPayloads := []string{
		"../host_secret.txt",
		"../../host_secret.txt",
		"sub/../../host_secret.txt",
		"....//host_secret.txt",
		"..%2fhost_secret.txt",
	}

	for _, p := range traversalPayloads {
		t.Run("read_file/"+p, func(t *testing.T) {
			out := r.Run(context.Background(), "read_file", json.RawMessage(`{"path":"`+p+`"}`))
			if strings.Contains(out, "SUPER_SECRET_DATA") {
				t.Fatalf("path traversal succeeded in read_file for %q: %s", p, out)
			}
		})

		t.Run("search_files/"+p, func(t *testing.T) {
			out := r.Run(context.Background(), "search_files", json.RawMessage(`{"pattern":"SUPER_SECRET","dir":"`+p+`"}`))
			if strings.Contains(out, "SUPER_SECRET_DATA") {
				t.Fatalf("path traversal succeeded in search_files for %q: %s", p, out)
			}
		})

		t.Run("list_dir/"+p, func(t *testing.T) {
			out := r.Run(context.Background(), "list_dir", json.RawMessage(`{"path":"`+p+`"}`))
			if !strings.HasPrefix(out, "Error") {
				t.Fatalf("path traversal in list_dir did not return Error for %q: %s", p, out)
			}
		})
	}
}

// 2. Absolute paths outside the workspace
func TestSecurity_AbsolutePathsRejected(t *testing.T) {
	dir := t.TempDir()
	r := mustRegistry(t, dir)

	absTarget := filepath.Join(filepath.Dir(dir), "outside.txt")
	_ = os.WriteFile(absTarget, []byte("OUTSIDE"), 0600)
	defer os.Remove(absTarget)

	absEscaped := filepath.ToSlash(absTarget)
	for _, tool := range []string{"read_file", "list_dir", "edit_file"} {
		out := r.Run(context.Background(), tool, json.RawMessage(`{"path":"`+absEscaped+`"}`))
		if strings.Contains(out, "OUTSIDE") {
			t.Fatalf("absolute path accepted by %s: %s", tool, out)
		}
	}
}

// 3. Sensitive paths denied
func TestSecurity_SensitivePathsDenied(t *testing.T) {
	dir := t.TempDir()
	r := mustRegistry(t, dir)

	sensitiveFiles := []string{
		".env",
		".env.production",
		".env.local",
		".git/config",
		".nabd/audit.log",
		"id_rsa",
		"id_ed25519",
		"server.key",
		"cert.pem",
	}

	for _, sf := range sensitiveFiles {
		fullPath := filepath.Join(dir, sf)
		_ = os.MkdirAll(filepath.Dir(fullPath), 0755)
		_ = os.WriteFile(fullPath, []byte("SENSITIVE_KEY_DATA"), 0600)

		// read_file
		out := r.Run(context.Background(), "read_file", json.RawMessage(`{"path":"`+sf+`"}`))
		if strings.Contains(out, "SENSITIVE_KEY_DATA") {
			t.Fatalf("sensitive file %s leaked via read_file: %s", sf, out)
		}

		// search_files
		sOut := r.Run(context.Background(), "search_files", json.RawMessage(`{"pattern":"SENSITIVE_KEY"}`))
		if strings.Contains(sOut, sf) || strings.Contains(sOut, "SENSITIVE_KEY_DATA") {
			t.Fatalf("sensitive file %s leaked via search_files: %s", sf, sOut)
		}
	}
}

// 4. Unauthorized write attempts (calling Run on write tools without approval)
func TestSecurity_UnauthorizedWriteAttemptsFailClosed(t *testing.T) {
	dir := t.TempDir()
	r := mustRegistry(t, dir)

	p := filepath.Join(dir, "target.txt")
	mustWrite(t, p, "initial content", 0644)

	// Attempt edit_file via Run()
	out := r.Run(context.Background(), "edit_file", json.RawMessage(`{"path":"target.txt","old_text":"initial content","new_text":"hacked content"}`))
	if !strings.Contains(out, "requires approval") {
		t.Fatalf("edit_file did not fail closed on unapproved Run: %s", out)
	}

	// Verify file was NOT modified
	data, _ := os.ReadFile(p)
	if string(data) != "initial content" {
		t.Fatalf("file modified without approval: %s", string(data))
	}

	// Attempt go_verify via Run()
	gvOut := r.Run(context.Background(), "go_verify", json.RawMessage(`{"pkg":"./..."}`))
	if !strings.Contains(gvOut, "requires approval") {
		t.Fatalf("go_verify did not fail closed on unapproved Run: %s", gvOut)
	}

	// Attempt mcp_call via Run()
	mcpOut := r.Run(context.Background(), "mcp_call", json.RawMessage(`{"command":"echo","tool":"test"}`))
	if !strings.Contains(mcpOut, "requires approval") {
		t.Fatalf("mcp_call did not fail closed on unapproved Run: %s", mcpOut)
	}
}

// 5. Token reuse (Replay attack protection)
func TestSecurity_ApprovalTokenSingleUse(t *testing.T) {
	dir := t.TempDir()
	r := mustRegistry(t, dir)

	p := filepath.Join(dir, "f.txt")
	mustWrite(t, p, "first", 0644)

	args := json.RawMessage(`{"path":"f.txt","old_text":"first","new_text":"second"}`)
	preview, err := r.PreviewApproval("edit_file", args)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := r.ApprovePreview("edit_file", args, preview.State)
	if err != nil {
		t.Fatal(err)
	}

	// First execution should succeed
	out1 := r.RunApproved(context.Background(), "edit_file", args, approval)
	if !strings.Contains(out1, "Successfully") {
		t.Fatalf("first execution failed: %s", out1)
	}

	// Prepare a second edit using the SAME token (replay attack)
	out2 := r.RunApproved(context.Background(), "edit_file", args, approval)
	if !strings.Contains(out2, "invalid or expired") {
		t.Fatalf("replayed token was accepted: %s", out2)
	}
}

// 6. Concurrent approval consumption race test
func TestSecurity_ConcurrentApprovalConsumption(t *testing.T) {
	dir := t.TempDir()
	r := mustRegistry(t, dir)

	p := filepath.Join(dir, "f.txt")
	mustWrite(t, p, "content", 0644)

	args := json.RawMessage(`{"path":"f.txt","old_text":"content","new_text":"new_content"}`)
	preview, err := r.PreviewApproval("edit_file", args)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := r.ApprovePreview("edit_file", args, preview.State)
	if err != nil {
		t.Fatal(err)
	}

	var successCount int32
	var wg sync.WaitGroup
	workers := 10
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			out := r.RunApproved(context.Background(), "edit_file", args, approval)
			if strings.Contains(out, "Successfully") {
				atomic.AddInt32(&successCount, 1)
			}
		}()
	}
	wg.Wait()

	// Exactly ONE worker must succeed. All other 9 must be rejected.
	if successCount != 1 {
		t.Fatalf("expected exactly 1 execution from single-use token, got %d", successCount)
	}
}

// 7. Audit failure fails-closed for write operations
func TestSecurity_AuditFailureFailsClosedForWrites(t *testing.T) {
	dir := t.TempDir()
	r := mustRegistry(t, dir)

	// Block audit log by creating .nabd as a file so directory cannot be created
	_ = r.auditLog.Close()
	if err := os.WriteFile(filepath.Join(dir, ".nabd"), []byte("not a directory"), 0644); err != nil {
		t.Fatal(err)
	}

	// Approving an edit must fail because audit logging the approval is mandatory
	args := json.RawMessage(`{"path":"a.txt","old_text":"x","new_text":"y"}`)
	_, err := r.Approve("edit_file", args)
	if err == nil {
		t.Fatal("Approve succeeded despite audit failure")
	}
}

// 8. Shadow Storage consistency and recovery across restarts
func TestSecurity_ShadowStoreRecoveryAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	// Session 1: Create backup
	store1, err := shadowstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store1.Backup("file.txt", []byte("original_bytes"), 0644, []byte("edited_bytes"))
	if err != nil {
		t.Fatal(err)
	}

	// Session 2: Fresh instance (simulating agent restart)
	store2, err := shadowstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}

	got, data, err := store2.Latest("file.txt")
	if err != nil {
		t.Fatalf("failed to retrieve latest after restart: %v", err)
	}
	if got.ID != entry.ID || string(data) != "original_bytes" {
		t.Fatalf("recovered data mismatch: got %s / %s", got.ID, string(data))
	}

	// Undo in Session 2
	if err := store2.MarkUndone(entry.ID); err != nil {
		t.Fatal(err)
	}

	// Verify Latest now returns ErrNotFound
	if _, _, err := store2.Latest("file.txt"); err != shadowstore.ErrNotFound {
		t.Fatalf("expected ErrNotFound after MarkUndone, got: %v", err)
	}
}

// 9. Registry shutdown cleans up active MCP sessions
func TestSecurity_RegistryShutdownCleansUpMCPSessions(t *testing.T) {
	workDir := t.TempDir()
	r, err := NewRegistry(workDir)
	if err != nil {
		t.Fatal(err)
	}

	// Populate simulated MCP session
	r.mcpSessionsMu.Lock()
	r.mcpSessions["dummy_cmd"] = &mcpSession{c: nil, closed: false}
	r.mcpSessionsMu.Unlock()

	// Close registry
	if err := r.Close(); err != nil {
		t.Fatalf("registry close failed: %v", err)
	}

	// Verify mcpSessions map is nil and closed
	r.mcpSessionsMu.Lock()
	if r.mcpSessions != nil {
		t.Fatal("mcpSessions map not nil after Registry.Close()")
	}
	r.mcpSessionsMu.Unlock()
}
