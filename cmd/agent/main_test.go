package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/amiraq1/nabd/internal/tools"
)

func testModel(t *testing.T) uiModel {
	t.Helper()
	reg, err := tools.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return initialModel(reg)
}

func TestTruncateKeepsValidUTF8(t *testing.T) {
	s := strings.Repeat("مرحبا", 20)
	got := truncate(s, 11)
	if !utf8.ValidString(got) {
		t.Fatalf("truncate produced invalid UTF-8: %q", got)
	}
	if len(got) > len(s) {
		t.Fatalf("truncate grew the string: %q", got)
	}
}

func TestApprovalPromptShowsArguments(t *testing.T) {
	m := testModel(t)
	tc := ToolCall{ID: "call_1", Type: "function"}
	tc.Function.Name = "edit_file"
	tc.Function.Arguments = `{"path":"secret.go","old_text":"a","new_text":"b"}`
	m.pendingTools = []ToolCall{tc}
	m.awaitingApproval = true

	view := m.View()
	if !strings.Contains(view, "secret.go") {
		t.Fatalf("approval prompt hides the tool arguments: %q", view)
	}
}

func TestIterationLimitAnswersToolCalls(t *testing.T) {
	m := testModel(t)
	m.busy = true
	m.iteration = maxIterations - 1

	tc := ToolCall{ID: "call_1", Type: "function"}
	tc.Function.Name = "read_file"
	tc.Function.Arguments = `{"path":"x"}`
	updated, _ := m.Update(llmResponseMsg{assistantMsg: Message{Role: "assistant", ToolCalls: []ToolCall{tc}}})
	um, ok := updated.(uiModel)
	if !ok {
		t.Fatalf("unexpected model type %T", updated)
	}

	for _, msg := range um.messages {
		if msg.Role == "tool" && msg.ToolCallID == "call_1" {
			return
		}
	}
	t.Fatal("assistant tool_call left unanswered after the iteration limit")
}
