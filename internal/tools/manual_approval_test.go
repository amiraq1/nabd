package tools

import (
	"encoding/json"
	"testing"
)

// autoApproveAllTag mirrors the audit tag the agent uses for ALL mode.
const autoApproveAllTag = "all"

// TestAutoApprovalIsRefusedForManualApprovalTools pins the shared enforcement
// point: no auto-approval entry point may authorize these two tools.
func TestAutoApprovalIsRefusedForManualApprovalTools(t *testing.T) {
	r := approvalRegistry(t, t.TempDir())
	t.Cleanup(func() { _ = r.Close() })

	for _, name := range []string{"go_verify", "mcp_call"} {
		if !r.RequiresManualApproval(name) {
			t.Fatalf("%s must require manual approval", name)
		}
		args := json.RawMessage(`{"pkg":"./..."}`)
		if name == "mcp_call" {
			args = json.RawMessage(`{"command":"probe","tool":"inspect"}`)
		}
		if _, err := r.ApproveAuto(name, args, autoApproveAllTag); err == nil {
			t.Fatalf("%s accepted auto-approval", name)
		}
		if name == "mcp_call" {
			if _, err := r.ApproveWithIdentityAuto(name, args, "synthetic-identity", autoApproveAllTag); err == nil {
				t.Fatal("mcp_call accepted auto-approval bound to a server identity")
			}
		}
		// The manual path must stay available.
		if name == "mcp_call" {
			if _, err := r.ApproveWithIdentity(name, args, "synthetic-identity"); err != nil {
				t.Fatalf("%s manual approval was refused: %v", name, err)
			}
			continue
		}
		if _, err := r.Approve(name, args); err != nil {
			t.Fatalf("%s manual approval was refused: %v", name, err)
		}
	}

	// Tools outside the list keep their previous auto-approval behaviour.
	if r.RequiresManualApproval("edit_file") {
		t.Fatal("edit_file must stay auto-approvable")
	}
	if _, err := r.ApproveAuto("edit_file", json.RawMessage(`{"path":"missing.txt","old_text":"a","new_text":"b"}`), autoApproveAllTag); err != nil {
		t.Fatalf("edit_file auto-approval broke: %v", err)
	}
}
