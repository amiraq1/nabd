package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/amiraq1/nabd/internal/tools"
)

func approvalTestModel(t *testing.T, dir string) uiModel {
	t.Helper()
	reg, err := tools.NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return initialModel(reg, &mockClient{})
}

func TestApprovalShowsFullArgumentsWithinLimit(t *testing.T) {
	m := approvalTestModel(t, t.TempDir())
	tail := "TAIL-MARKER-1234567890"
	args := `{"command":"` + strings.Repeat("a", 1000) + tail + `","tool":"x"}`
	tc := ToolCall{ID: "call_small", Type: "function"}
	tc.Function.Name = "mcp_call"
	tc.Function.Arguments = args
	m.pendingTools = []ToolCall{tc}
	next, _ := m.advance()
	v := next.(uiModel)
	if !v.awaitingApproval || !strings.Contains(v.View(), tail) {
		t.Fatal("approval did not show the complete command")
	}
}

func TestOversizedApprovalIsRejectedWithoutExecution(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}

	m := approvalTestModel(t, dir)
	m.busy = true
	args := fmt.Sprintf(`{"path":"a.txt","old_text":"old","new_text":%q}`, strings.Repeat("x", maxApprovalArgs+100))
	tc := ToolCall{ID: "call_big", Type: "function"}
	tc.Function.Name = "edit_file"
	tc.Function.Arguments = args

	updated, _ := m.Update(llmResponseMsg{assistantMsg: Message{Role: "assistant", ToolCalls: []ToolCall{tc}}})
	um, ok := updated.(uiModel)
	if !ok {
		t.Fatalf("unexpected model type %T", updated)
	}
	if um.awaitingApproval {
		t.Fatal("oversized edit is still awaiting approval")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old\n" {
		t.Fatalf("file changed: %q", got)
	}

	var reply *Message
	for i := range um.messages {
		if um.messages[i].Role == "tool" && um.messages[i].ToolCallID == tc.ID {
			reply = &um.messages[i]
		}
	}
	if reply == nil {
		t.Fatalf("oversized tool_call left unanswered (%d bytes)", len(args))
	}
	if !strings.Contains(reply.Content, "smaller edits") {
		t.Fatalf("tool reply does not tell the model to split the edit: %q", reply.Content)
	}
	if !strings.Contains(reply.Content, fmt.Sprintf("%d bytes", len(args))) {
		t.Fatalf("tool reply omits the argument size: %q", reply.Content)
	}
}

func TestApprovalArabicAtLimitIsValidUTF8(t *testing.T) {
	m := approvalTestModel(t, t.TempDir())
	tc := ToolCall{ID: "call_ar", Type: "function"}
	tc.Function.Name = "mcp_call"
	tc.Function.Arguments = `{"command":"` + strings.Repeat("م", maxApprovalArgs/2+100) + `","tool":"x"}`
	m.pendingTools = []ToolCall{tc}
	next, _ := m.advance()
	v := next.(uiModel)
	if v.awaitingApproval {
		t.Fatal("oversized command was approvable")
	}
	if !utf8.ValidString(v.View()) {
		t.Fatal("invalid UTF-8")
	}
}

func TestPR7ApprovalRejectsControlCharactersInCommand(t *testing.T) {
	m := approvalTestModel(t, t.TempDir())
	tc := ToolCall{ID: "control", Type: "function"}
	tc.Function.Name = "mcp_call"
	tc.Function.Arguments = `{"command":"safe\nspoof","tool":"x"}`
	m.pendingTools = []ToolCall{tc}
	next, _ := m.advance()
	v := next.(uiModel)
	if v.awaitingApproval {
		t.Fatal("command with line break was approvable")
	}
}
