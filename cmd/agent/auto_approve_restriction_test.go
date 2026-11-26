package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestAutoApproveAllDoesNotBypassManualTools is the execution-gate test for the
// two tools that must always be approved by a human, including with ALL active.
func TestAutoApproveAllDoesNotBypassManualTools(t *testing.T) {
	m := newTestAgentModel(t, t.TempDir(), AutoApproveAll)
	m.toolQueueMode = AutoApproveAll

	cases := []struct {
		name string
		args string
	}{
		{"go_verify", `{"pkg":"./..."}`},
		{"mcp_call", `{"command":"probe","tool":"inspect"}`},
	}
	for _, tc := range cases {
		toolCall := ToolCall{ID: "call-" + tc.name, Type: "function"}
		toolCall.Function.Name = tc.name
		toolCall.Function.Arguments = tc.args

		m.pendingTools = []ToolCall{toolCall}
		m.awaitingApproval = false
		m.approval = approvalPreview{}

		next, cmd := m.advance()
		m = next.(uiModel)

		if cmd != nil {
			t.Errorf("%s: a tool launched automatically (%v), it must wait for a manual decision", tc.name, cmd)
		}
		if !m.awaitingApproval {
			t.Errorf("%s: auto-approval ALL bypassed the manual execution gate", tc.name)
		}
		if m.approval.Summary == "" {
			t.Errorf("%s: no approval preview was rendered for the manual prompt", tc.name)
		}
	}
}

func TestAutoAllRequiresExplicitConfirmation(t *testing.T) {
	m := newTestAgentModel(t, t.TempDir(), AutoApproveOff)

	next, _ := m.handleAutoCommand("all")
	m = next.(uiModel)
	if m.autoApprove != AutoApproveOff {
		t.Fatalf("/auto all changed the mode before confirmation: %s", m.autoApprove)
	}
	if m.autoConfirm == nil {
		t.Fatal("/auto all did not raise a confirmation request")
	}

	// Esc cancels and keeps the previous mode.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(uiModel)
	if m.autoConfirm != nil {
		t.Fatal("Esc left the confirmation pending")
	}
	if m.autoApprove != AutoApproveOff {
		t.Fatalf("Esc enabled ALL: %s", m.autoApprove)
	}

	// An unconfirmed Enter cancels as well.
	next, _ = m.handleAutoCommand("all")
	m = next.(uiModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(uiModel)
	if m.autoApprove != AutoApproveOff {
		t.Fatalf("Enter enabled ALL without an explicit confirmation: %s", m.autoApprove)
	}

	// Any other key cancels.
	next, _ = m.handleAutoCommand("all")
	m = next.(uiModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = next.(uiModel)
	if m.autoApprove != AutoApproveOff {
		t.Fatalf("an unrelated key enabled ALL: %s", m.autoApprove)
	}

	// An explicit y enables it and the warning names the exceptions.
	next, _ = m.handleAutoCommand("all")
	m = next.(uiModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(uiModel)
	if m.autoApprove != AutoApproveAll {
		t.Fatalf("explicit y did not enable ALL: %s", m.autoApprove)
	}
	if m.autoConfirm != nil {
		t.Fatal("confirmation state survived a successful confirmation")
	}
	warning := m.messages[len(m.messages)-1].Content
	if !strings.Contains(warning, "WARNING") || !strings.Contains(warning, "go_verify and mcp_call") {
		t.Fatalf("ALL warning does not name the exceptions: %s", warning)
	}
}

// TestModeChangeDoesNotAuthorizeQueuedTools proves a mode change is not applied
// retroactively to tool calls that were already queued under a stricter mode.
func TestModeChangeDoesNotAuthorizeQueuedTools(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newTestAgentModel(t, dir, AutoApproveOff)
	toolCall := ToolCall{ID: "call-edit", Type: "function"}
	toolCall.Function.Name = "edit_file"
	toolCall.Function.Arguments = `{"path":"note.txt","old_text":"hello\n","new_text":"goodbye\n"}`
	m.pendingTools = []ToolCall{toolCall}
	m.toolQueueMode = AutoApproveOff

	next, _ := m.handleAutoCommand("all")
	m = next.(uiModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(uiModel)
	if m.autoApprove != AutoApproveAll {
		t.Fatalf("setup failed, mode = %s", m.autoApprove)
	}

	next, _ = m.advance()
	m = next.(uiModel)
	if !m.awaitingApproval {
		t.Fatal("a tool queued before the mode change was auto-approved retroactively")
	}

	// Disabling ALL must also keep an already-queued call manual.
	m.awaitingApproval = false
	m.toolQueueMode = AutoApproveAll
	next, _ = m.handleAutoCommand("off")
	m = next.(uiModel)
	next, _ = m.advance()
	m = next.(uiModel)
	if !m.awaitingApproval {
		t.Fatal("disabling auto-approval did not take effect for a queued tool call")
	}
}
