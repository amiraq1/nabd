package main

import (
	"fmt"
	"strings"
	"testing"
)

func mcpUIToolCall(id, args string) ToolCall {
	tc := ToolCall{ID: id, Type: "function"}
	tc.Function.Name = "mcp_call"
	tc.Function.Arguments = args
	return tc
}

func TestMCPCallApprovalShowsCommand(t *testing.T) {
	m := testModel(t)
	command := "/data/data/com.termux/files/usr/bin/true"
	args := fmt.Sprintf(`{"command":%q,"tool":"x"}`, command)

	m.busy = true
	updated, _ := m.Update(llmResponseMsg{assistantMsg: Message{Role: "assistant", ToolCalls: []ToolCall{mcpUIToolCall("call_mcp", args)}}})
	um, ok := updated.(uiModel)
	if !ok {
		t.Fatalf("unexpected model type %T", updated)
	}
	if !um.awaitingApproval {
		t.Fatal("mcp_call did not require approval")
	}
	if !strings.Contains(um.View(), command) {
		t.Fatalf("approval view hides the command: %q", um.View())
	}
}

func TestMCPCallOversizedCommandRejected(t *testing.T) {
	m := testModel(t)
	command := strings.Repeat("a", maxApprovalArgs+100)
	args := fmt.Sprintf(`{"command":%q,"tool":"x"}`, command)

	m.busy = true
	updated, _ := m.Update(llmResponseMsg{assistantMsg: Message{Role: "assistant", ToolCalls: []ToolCall{mcpUIToolCall("call_big_mcp", args)}}})
	um, ok := updated.(uiModel)
	if !ok {
		t.Fatalf("unexpected model type %T", updated)
	}
	if um.awaitingApproval {
		t.Fatal("oversized mcp_call was not rejected")
	}
	var reply string
	for _, msg := range um.messages {
		if msg.Role == "tool" && msg.ToolCallID == "call_big_mcp" {
			reply = msg.Content
		}
	}
	if !strings.Contains(reply, "smaller edits") {
		t.Fatalf("missing oversized rejection reply: %q", reply)
	}
}
