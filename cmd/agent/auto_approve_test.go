package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/amiraq1/nabd/internal/tools"
)

func newTestAgentModel(t *testing.T, dir string, mode AutoApproveMode) uiModel {
	t.Helper()
	reg, err := tools.NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() {
		_ = reg.Close()
	})
	m := initialModel(reg, &mockClient{}, mode)
	return m
}

func TestParseAutoApproveMode(t *testing.T) {
	tests := []struct {
		input   string
		want    AutoApproveMode
		wantErr bool
	}{
		{"", AutoApproveOff, false},
		{"   ", AutoApproveOff, false},
		{"off", AutoApproveOff, false},
		{"OFF", AutoApproveOff, false},
		{"  Off  ", AutoApproveOff, false},
		{"readonly", AutoApproveReadOnly, false},
		{"ReadOnly", AutoApproveReadOnly, false},
		{"  READONLY  ", AutoApproveReadOnly, false},
		{"all", AutoApproveAll, false},
		{"ALL", AutoApproveAll, false},
		{"  All ", AutoApproveAll, false},
		// Invalid values
		{"yes", "", true},
		{"no", "", true},
		{"true", "", true},
		{"false", "", true},
		{"1", "", true},
		{"0", "", true},
		{"always", "", true},
		{"alll", "", true},
		{"read_only", "", true},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.input), func(t *testing.T) {
			got, err := ParseAutoApproveMode(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseAutoApproveMode(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ParseAutoApproveMode(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAutoApprove_OffMode(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello world\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newTestAgentModel(t, dir, AutoApproveOff)

	// Step 1: Tool requiring approval (edit_file) must stop at awaitingApproval
	tc := ToolCall{
		ID: "call_edit",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{
			Name:      "edit_file",
			Arguments: `{"path":"test.txt","old_text":"hello world\n","new_text":"hello nabd\n"}`,
		},
	}
	m.pendingTools = []ToolCall{tc}

	nextM, cmd := m.advance()
	m = nextM.(uiModel)
	if cmd != nil {
		t.Fatalf("expected nil cmd awaiting manual approval, got %v", cmd)
	}
	if !m.awaitingApproval {
		t.Fatalf("expected awaitingApproval = true in off mode")
	}

	// Step 2: Tool execution result must stop at pendingDisclosure
	m.awaitingApproval = false
	toolMsg := Message{Role: "tool", ToolCallID: "call_1", Content: "file content"}
	nextM, _ = m.Update(toolExecutedMsg{toolMsg: toolMsg})
	m = nextM.(uiModel)
	if m.pendingDisclosure == nil {
		t.Fatalf("expected pendingDisclosure != nil in off mode")
	}
}

func TestAutoApprove_ReadOnlyMode(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("clean content\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newTestAgentModel(t, dir, AutoApproveReadOnly)

	// Case A: ReadOnly tool (read_file) with clean content -> automatically disclosed!
	tcRead := ToolCall{
		ID: "call_read",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{
			Name:      "read_file",
			Arguments: `{"path":"test.txt"}`,
		},
	}
	m.pendingTools = []ToolCall{tcRead}

	toolMsgClean := Message{Role: "tool", ToolCallID: "call_read", Content: "clean content"}
	nextM, _ := m.Update(toolExecutedMsg{toolMsg: toolMsgClean})
	m = nextM.(uiModel)
	if m.pendingDisclosure != nil {
		t.Fatalf("expected clean read_file to be auto-disclosed in readonly mode, got pendingDisclosure != nil")
	}
	if len(m.messages) == 0 || m.messages[len(m.messages)-1].Content != "clean content" {
		t.Fatalf("expected clean content appended to messages")
	}

	// Case B: ReadOnly tool with potential secret -> must NOT auto-disclose, stops for manual review!
	m.pendingTools = []ToolCall{tcRead}
	secretContent := "api_key = sk-abcdef12345678901234"
	toolMsgSecret := Message{Role: "tool", ToolCallID: "call_read", Content: secretContent}
	nextM, _ = m.Update(toolExecutedMsg{toolMsg: toolMsgSecret})
	m = nextM.(uiModel)
	if m.pendingDisclosure == nil {
		t.Fatalf("expected secret-bearing read_file to require manual disclosure in readonly mode, got nil")
	}

	// Case C: Non-ReadOnly tool (edit_file) -> still requires execution approval!
	tcEdit := ToolCall{
		ID: "call_edit",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{
			Name:      "edit_file",
			Arguments: `{"path":"test.txt","old_text":"clean content\n","new_text":"new\n"}`,
		},
	}
	m.pendingTools = []ToolCall{tcEdit}
	m.pendingDisclosure = nil
	nextM, cmd := m.advance()
	m = nextM.(uiModel)
	if cmd != nil || !m.awaitingApproval {
		t.Fatalf("expected edit_file to require approval in readonly mode")
	}
}

func TestAutoApprove_AllMode(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("line1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newTestAgentModel(t, dir, AutoApproveAll)

	// Case A: edit_file automatically approved and token generated
	tcEdit := ToolCall{
		ID: "call_edit",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{
			Name:      "edit_file",
			Arguments: `{"path":"test.txt","old_text":"line1\n","new_text":"line2\n"}`,
		},
	}
	m.pendingTools = []ToolCall{tcEdit}

	nextM, cmd := m.advance()
	m = nextM.(uiModel)
	if m.awaitingApproval {
		t.Fatalf("expected awaitingApproval = false in all mode")
	}
	if cmd == nil {
		t.Fatalf("expected non-nil cmd executing tool in all mode")
	}

	// Case B: toolExecutedMsg automatically disclosed even with secrets
	m.pendingTools = []ToolCall{tcEdit}
	toolMsg := Message{Role: "tool", ToolCallID: "call_edit", Content: "api_key = sk-1234567890abcdef"}
	nextM, _ = m.Update(toolExecutedMsg{toolMsg: toolMsg})
	m = nextM.(uiModel)
	if m.pendingDisclosure != nil {
		t.Fatalf("expected pendingDisclosure = nil in all mode")
	}
	if m.messages[len(m.messages)-1].Content != "api_key = sk-1234567890abcdef" {
		t.Fatalf("expected tool message appended directly")
	}

	// Case C: non-displayable characters fail-closed even in all mode
	m.pendingTools = []ToolCall{tcEdit}
	toolMsgBad := Message{Role: "tool", ToolCallID: "call_edit", Content: "bad \x1b[31mcolor\x1b[0m"}
	nextM, _ = m.Update(toolExecutedMsg{toolMsg: toolMsgBad})
	m = nextM.(uiModel)
	lastMsg := m.messages[len(m.messages)-1]
	if !strings.Contains(lastMsg.Content, "Tool disclosure blocked") {
		t.Fatalf("expected disclosure blocked for non-displayable control chars, got: %s", lastMsg.Content)
	}
}

func TestAutoApprove_Command(t *testing.T) {
	dir := t.TempDir()
	m := newTestAgentModel(t, dir, AutoApproveOff)

	// 1. /auto without args displays status
	nextM, _ := m.handleAutoCommand("")
	m = nextM.(uiModel)
	lastMsg := m.messages[len(m.messages)-1].Content
	if !strings.Contains(lastMsg, "Auto-approval mode: off") {
		t.Fatalf("unexpected /auto response: %s", lastMsg)
	}

	// 2. /auto all must wait for an explicit confirmation and explain the bypass
	nextM, _ = m.handleAutoCommand("all")
	m = nextM.(uiModel)
	if m.autoApprove != AutoApproveOff {
		t.Fatalf("mode changed before the user confirmed /auto all: %s", m.autoApprove)
	}
	if m.autoConfirm == nil {
		t.Fatal("expected /auto all to raise a pending confirmation")
	}
	lastMsg = m.messages[len(m.messages)-1].Content
	if !strings.Contains(lastMsg, "bypasses") {
		t.Fatalf("expected the confirmation to explain what it bypasses: %s", lastMsg)
	}

	// 3. /auto readonly changes mode
	nextM, _ = m.handleAutoCommand("readonly")
	m = nextM.(uiModel)
	if m.autoApprove != AutoApproveReadOnly {
		t.Fatalf("expected mode = readonly, got %s", m.autoApprove)
	}

	// 4. /auto invalid returns error
	nextM, _ = m.handleAutoCommand("invalid_mode")
	m = nextM.(uiModel)
	if m.autoApprove != AutoApproveReadOnly { // remains unchanged
		t.Fatalf("expected mode unchanged after invalid input")
	}
	lastMsg = m.messages[len(m.messages)-1].Content
	if !strings.Contains(lastMsg, "Error:") {
		t.Fatalf("expected error message on invalid input: %s", lastMsg)
	}

	// 5. /auto blocked while approval pending
	m.awaitingApproval = true
	nextM, _ = m.handleAutoCommand("all")
	m = nextM.(uiModel)
	lastMsg = m.messages[len(m.messages)-1].Content
	if !strings.Contains(lastMsg, "Cannot change auto-approval mode while a tool approval or disclosure is pending") {
		t.Fatalf("expected blocked change during pending approval, got: %s", lastMsg)
	}
}

func TestAutoApprove_HeaderWidths(t *testing.T) {
	dir := t.TempDir()
	mOff := newTestAgentModel(t, dir, AutoApproveOff)
	mReadOnly := newTestAgentModel(t, dir, AutoApproveReadOnly)
	mAll := newTestAgentModel(t, dir, AutoApproveAll)

	widths := []int{36, 40, 45, 60, 80, 120}

	for _, w := range widths {
		t.Run(fmt.Sprintf("width_%d", w), func(t *testing.T) {
			for _, m := range []struct {
				name  string
				model uiModel
				badge string
			}{
				{"off", mOff, ""},
				{"readonly", mReadOnly, "Auto: ReadOnly"},
				{"all", mAll, "Auto: ALL"},
			} {
				header := renderHeader(&m.model, w)
				lines := strings.Split(strings.TrimRight(header, "\n"), "\n")
				for lineIdx, line := range lines {
					lineLen := lipgloss.Width(line)
					if lineLen > w {
						t.Errorf("[%s width %d line %d] length %d exceeds width %d:\n%q", m.name, w, lineIdx, lineLen, w, line)
					}
				}
				if m.badge != "" && w >= 60 {
					if !strings.Contains(header, m.badge) {
						t.Errorf("[%s width %d] expected badge %q in header:\n%s", m.name, w, m.badge, header)
					}
				}
			}
		})
	}
}

func TestAutoApprove_StatusOutput(t *testing.T) {
	dir := t.TempDir()
	mOff := newTestAgentModel(t, dir, AutoApproveOff)
	if !strings.Contains(mOff.formatStatus(), "Auto-approve: off") {
		t.Fatalf("expected Auto-approve: off in status:\n%s", mOff.formatStatus())
	}

	mRO := newTestAgentModel(t, dir, AutoApproveReadOnly)
	if !strings.Contains(mRO.formatStatus(), "Auto-approve: readonly") {
		t.Fatalf("expected Auto-approve: readonly in status:\n%s", mRO.formatStatus())
	}

	mAll := newTestAgentModel(t, dir, AutoApproveAll)
	if !strings.Contains(mAll.formatStatus(), "Auto-approve: all") {
		t.Fatalf("expected Auto-approve: all in status:\n%s", mAll.formatStatus())
	}
}
