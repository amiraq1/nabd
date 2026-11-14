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
	return initialModel(reg)
}

func TestApprovalShowsFullArgumentsWithinLimit(t *testing.T) {
	m := approvalTestModel(t, t.TempDir())
	tail := "TAIL-MARKER-1234567890"
	args := `{"path":"a.go","old_text":"x","new_text":"` + strings.Repeat("a", 1000) + tail + `"}`
	tc := ToolCall{ID: "call_small", Type: "function"}
	tc.Function.Name = "edit_file"
	tc.Function.Arguments = args
	m.pendingTools = []ToolCall{tc}
	m.awaitingApproval = true

	view := m.View()
	if !strings.Contains(view, tail) {
		t.Fatalf("approval prompt hid part of the arguments (%d bytes)", len(args))
	}
	if strings.Contains(view, "bytes not shown") {
		t.Fatalf("unexpected truncation notice for %d bytes", len(args))
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
	prefix := `{"path":"x.go","old_text":"`
	if len(prefix)%2 == 0 {
		prefix += "a"
	}
	args := prefix + strings.Repeat("م", maxApprovalArgs/2+100) + `","new_text":"y"}`
	tc := ToolCall{ID: "call_ar", Type: "function"}
	tc.Function.Name = "edit_file"
	tc.Function.Arguments = args
	m.pendingTools = []ToolCall{tc}
	m.awaitingApproval = true

	view := m.View()
	if !utf8.ValidString(view) {
		t.Fatal("approval view is not valid UTF-8")
	}
	if !strings.Contains(view, "bytes not shown") {
		t.Fatalf("approval view has no hidden-byte count for %d bytes", len(args))
	}
}
