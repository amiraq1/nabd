package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A token is issued only after the preview; changes after token issuance must
// also be rejected, not just changes before approval.
func TestPR7TokenRejectsChangeAfterApproval(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	mustWrite(t, target, "old\n", 0600)
	r := mustRegistry(t, dir)
	args := json.RawMessage(`{"path":"a.txt","old_text":"old","new_text":"new"}`)
	preview, err := r.PreviewApproval("edit_file", args)
	if err != nil {
		t.Fatal(err)
	}
	token, err := r.ApprovePreview("edit_file", args, preview.State)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(target, []byte("changed old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out := r.RunApproved(context.Background(), "edit_file", args, token)
	events, _ := readAuditLog(t, dir)
	if len(events) == 0 || events[len(events)-1].Event != "failed" {
		t.Fatal("rejected post-approval change was not recorded as failed")
	}
	if !strings.Contains(out, "file changed since approval") {
		t.Fatalf("changed file was not rejected: %q", out)
	}
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "changed old\n" {
		t.Fatal("changed file was overwritten")
	}
}

func TestPR7MCPCallRunNeverUsesCallbackApproval(t *testing.T) {
	dir := t.TempDir()
	called := false
	executed := false
	r, err := NewRegistry(dir, func(string, string) bool { called = true; return true })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	spec := r.handlers["mcp_call"]
	spec.Execute = func(context.Context, json.RawMessage) (string, error) { executed = true; return "unexpected", nil }
	r.handlers["mcp_call"] = spec
	out := r.Run(context.Background(), "mcp_call", json.RawMessage(`{"command":"x","tool":"x"}`))
	if !strings.Contains(out, "RunApproved") || called || executed {
		t.Fatal("mcp_call bypassed the RunApproved gate")
	}
}
