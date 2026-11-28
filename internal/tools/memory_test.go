package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amiraq1/nabd/internal/audit"
	"github.com/amiraq1/nabd/internal/memory"
)

func TestMemoryToolsApprovalBindingManualOnlyAndAudit(t *testing.T) {
	dir, reg := newTestRegistry(t)
	ctx := context.Background()

	if !reg.Has("memory_save") || !reg.Has("memory_search") {
		t.Fatal("memory_save or memory_search not registered")
	}
	if reg.RequiresApproval("memory_search") || !reg.IsReadOnly("memory_search") {
		t.Fatal("memory_search must be ReadOnly")
	}
	if !reg.RequiresApproval("memory_save") || reg.IsReadOnly("memory_save") {
		t.Fatal("memory_save must require approval")
	}
	if !reg.RequiresManualApproval("memory_save") {
		t.Fatal("memory_save must require manual approval")
	}

	rawArgs := json.RawMessage(`{"title":"  Keep defaults  ","body":"Use BM25 locally.\r\nSecond line.","tags":["b","a","b"]}`)
	canon, identity, err := reg.CanonicalMemorySave(rawArgs)
	if err != nil {
		t.Fatalf("CanonicalMemorySave: %v", err)
	}
	if canon.Kind != "note" || canon.Title != "Keep defaults" || strings.Contains(canon.Body, "\r") {
		t.Fatalf("unexpected canonicalization: %+v", canon)
	}
	if len(canon.Tags) != 2 || canon.Tags[0] != "a" || canon.Tags[1] != "b" {
		t.Fatalf("unexpected canonical tags: %+v", canon.Tags)
	}

	// Auto-approval ALL must be refused on every entry point.
	if _, err := reg.ApproveAuto("memory_save", rawArgs, "all"); err == nil {
		t.Fatal("ApproveAuto unexpectedly allowed memory_save in all mode")
	}
	if _, err := reg.ApproveWithIdentityAuto("memory_save", rawArgs, identity, "all"); err == nil {
		t.Fatal("ApproveWithIdentityAuto unexpectedly allowed memory_save in all mode")
	}

	// Run without RunApproved must fail closed.
	if out := reg.Run(ctx, "memory_save", rawArgs); !strings.Contains(out, "requires approval") && !strings.Contains(out, "no approver") {
		t.Fatalf("Run without approval succeeded: %s", out)
	}

	// Mismatched identity must be refused.
	if _, err := reg.ApproveWithIdentity("memory_save", rawArgs, "memsave:sha256:wrong"); err == nil {
		t.Fatal("ApproveWithIdentity accepted wrong identity")
	}

	app, err := reg.ApproveWithIdentity("memory_save", rawArgs, identity)
	if err != nil {
		t.Fatalf("ApproveWithIdentity: %v", err)
	}

	// Tampering with arguments after approval must fail without executing; token remains unused until consumed by exact args.
	tampered := json.RawMessage(`{"title":"Keep defaults","body":"Tampered body","tags":["a","b"]}`)
	if out := reg.RunApproved(ctx, "memory_save", tampered, app); !strings.Contains(out, "invalid or expired") {
		t.Fatalf("RunApproved accepted tampered args: %s", out)
	}

	// Original approved args succeed once, then token reuse must be rejected.
	out := reg.RunApproved(ctx, "memory_save", rawArgs, app)
	var saveRes memorySaveResult
	if err := json.Unmarshal([]byte(out), &saveRes); err != nil {
		t.Fatalf("memory_save did not return JSON: %s (%v)", out, err)
	}
	if err := memory.ValidateID(saveRes.ID); err != nil || saveRes.Kind != "note" {
		t.Fatalf("unexpected memory_save result: %+v", saveRes)
	}
	if reuseOut := reg.RunApproved(ctx, "memory_save", rawArgs, app); !strings.Contains(reuseOut, "invalid or expired") {
		t.Fatalf("RunApproved allowed token reuse: %s", reuseOut)
	}

	// Verify memory_search retrieves saved memory tagged as untrusted.
	searchOut := reg.Run(ctx, "memory_search", json.RawMessage(`{"query":"BM25"}`))
	var searchResp memory.SearchResponse
	if err := json.Unmarshal([]byte(searchOut), &searchResp); err != nil {
		t.Fatalf("memory_search output invalid JSON: %s (%v)", searchOut, err)
	}
	if searchResp.SecurityNotice != memory.SecurityNotice || searchResp.Returned != 1 || searchResp.Results[0].ID != saveRes.ID {
		t.Fatalf("unexpected memory_search response: %+v", searchResp)
	}

	// Verify audit.log records phases without leaking title/body/tags, and records id only on applied.
	auditBytes, err := os.ReadFile(filepath.Join(dir, ".nabd", "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	auditText := string(auditBytes)
	if strings.Contains(auditText, "Keep defaults") || strings.Contains(auditText, "Use BM25 locally") {
		t.Fatal("audit.log leaked memory title or body")
	}
	var seenExecuting, seenApplied bool
	for _, line := range strings.Split(strings.TrimSpace(auditText), "\n") {
		var ev audit.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("invalid audit line: %v", err)
		}
		if ev.Tool == "memory_save" && ev.Event == "executing" {
			seenExecuting = true
			if strings.Contains(ev.Detail, "mem_") {
				t.Fatalf("executing event claimed ID before generation: %s", ev.Detail)
			}
		}
		if ev.Tool == "memory_save" && ev.Event == "applied" {
			seenApplied = true
			if !strings.Contains(ev.Detail, "id="+saveRes.ID) || !strings.Contains(ev.Detail, "kind=note") {
				t.Fatalf("applied event missing id/kind detail: %s", ev.Detail)
			}
		}
	}
	if !seenExecuting || !seenApplied {
		t.Fatalf("missing memory_save audit stages: executing=%v applied=%v", seenExecuting, seenApplied)
	}
}

func TestMemoryToolsSecretRejectionAndNabdIsolation(t *testing.T) {
	dir, reg := newTestRegistry(t)
	ctx := context.Background()

	fakeSecret := "sk-" + strings.Repeat("Z", 24)
	secretArgs, _ := json.Marshal(map[string]any{
		"kind":  "note",
		"title": "Do not leak",
		"body":  "token=" + fakeSecret,
	})
	if _, _, err := reg.CanonicalMemorySave(secretArgs); err == nil {
		t.Fatal("CanonicalMemorySave accepted secret")
	}
	if _, err := reg.RememberMemory(ctx, memory.SaveInput{Title: "Secret", Body: fakeSecret}); err == nil {
		t.Fatal("RememberMemory accepted secret")
	}
	if _, err := os.Stat(filepath.Join(dir, ".nabd", "memory", "entries.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("entries.jsonl created for rejected secret (err=%v)", err)
	}

	// Ensure audit.log does not contain the secret or title.
	if auditBytes, err := os.ReadFile(filepath.Join(dir, ".nabd", "audit.log")); err == nil {
		if strings.Contains(string(auditBytes), fakeSecret) || strings.Contains(string(auditBytes), "Secret") {
			t.Fatal("audit.log leaked rejected secret or title")
		}
	}

	// Save a legitimate entry so .nabd/memory exists, then verify file tools and sandbox masks isolate it.
	if _, err := reg.RememberMemory(ctx, memory.SaveInput{Kind: "decision", Title: "Public decision", Body: "Keep .nabd isolated."}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"read_file", map[string]any{"path": ".nabd/memory/entries.jsonl"}},
		{"list_dir", map[string]any{"path": ".nabd/memory"}},
		{"create_file", map[string]any{"path": ".nabd/memory/evil.txt", "content": "x"}},
		{"edit_file", map[string]any{"path": ".nabd/memory/entries.jsonl", "old_text": "Public", "new_text": "X"}},
		{"grep_search", map[string]any{"query": "Public decision"}},
	} {
		out := runTool(t, reg, tc.tool, tc.args)
		if strings.Contains(out, "Public decision") {
			t.Fatalf("%s accessed .nabd/memory: %s", tc.tool, out)
		}
	}
	dirs, _, err := collectSensitiveWorkspaceMasks(dir)
	if err != nil {
		t.Fatal(err)
	}
	foundNabd := false
	for _, d := range dirs {
		if d == ".nabd" {
			foundNabd = true
		}
	}
	if !foundNabd {
		t.Fatalf(".nabd missing from sandbox mask dirs: %v", dirs)
	}
}
