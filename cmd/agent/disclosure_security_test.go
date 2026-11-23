package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFileDisclosureRequiresReviewAndApproval(t *testing.T) {
	_, m := testModel(t)
	m.destination = "https://model.example/v1"
	m.viewport.Width, m.viewport.Height = 20, 3
	tc := testToolCall("read_file", `{"path":"secret.txt"}`)
	tc.ID = "read-1"
	m.pendingTools = []ToolCall{tc}
	secret := strings.Repeat("confidential-", 20)
	updated, cmd := m.Update(toolExecutedMsg{toolMsg: Message{Role: "tool", ToolCallID: tc.ID, Content: secret}})
	m = updated.(uiModel)
	if cmd != nil || m.pendingDisclosure == nil {
		t.Fatal("file output was not held for consent")
	}
	for _, msg := range m.messages {
		if strings.Contains(msg.Content, secret) {
			t.Fatal("disclosed before consent")
		}
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(uiModel)
	if cmd != nil || m.pendingDisclosure == nil {
		t.Fatal("approved without reviewing to end")
	}
	m.viewport.GotoBottom()
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(uiModel)
	for _, msg := range m.messages {
		if strings.Contains(msg.Content, secret) {
			t.Fatal("rejected output entered history")
		}
	}
}

func TestFileDisclosureApprovalRetainsExactPayload(t *testing.T) {
	_, m := testModel(t)
	m.destination = "https://model.example/v1"
	tc := testToolCall("read_file", `{"path":"a.txt"}`)
	tc.ID = "read-2"
	m.pendingTools = []ToolCall{tc}
	msg := Message{Role: "tool", ToolCallID: tc.ID, Content: "exact file bytes\n"}
	updated, _ := m.Update(toolExecutedMsg{toolMsg: msg})
	m = updated.(uiModel)
	m.viewport.GotoBottom()
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(uiModel)
	if m.pendingDisclosure != nil || len(m.messages) == 0 || m.messages[len(m.messages)-1].Content != msg.Content {
		t.Fatal("approved output differs")
	}
}

func TestFileDisclosureResizeRequiresRereview(t *testing.T) {
	_, m := testModel(t)
	m.viewport.Width = 20
	m.viewport.Height = 3
	tc := testToolCall("read_file", `{"path":"a.txt"}`)
	tc.ID = "read-3"
	m.pendingTools = []ToolCall{tc}
	updated, _ := m.Update(toolExecutedMsg{toolMsg: Message{Role: "tool", ToolCallID: tc.ID, Content: strings.Repeat("x", 200)}})
	m = updated.(uiModel)
	m.viewport.GotoBottom()
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	m = updated.(uiModel)
	if m.viewport.AtBottom() {
		t.Fatal("resize bypassed review")
	}
}

func TestUnsandboxedToolsDisabled(t *testing.T) {
	_, m := testModel(t)
	disableUnsandboxedTools(m.registry)
	for _, name := range []string{"mcp_call", "go_verify"} {
		if m.registry.Has(name) {
			t.Fatalf("%s still available", name)
		}
		for _, def := range m.registry.OpenAITools() {
			fn, _ := def["function"].(map[string]any)
			if fn["name"] == name {
				t.Fatalf("%s still advertised", name)
			}
		}
	}
}

func TestAllToolOutputsRequireDisclosure(t *testing.T) {
	for _, tool := range []string{"list_dir", "edit_file", "go_verify", "mcp_call", "unknown_tool"} {
		t.Run(tool, func(t *testing.T) {
			_, m := testModel(t)
			tc := testToolCall(tool, `{}`)
			tc.ID = tool + "-id"
			m.pendingTools = []ToolCall{tc}
			payload := "project-secret-" + tool
			updated, _ := m.Update(toolExecutedMsg{toolMsg: Message{Role: "tool", ToolCallID: tc.ID, Content: payload}})
			m = updated.(uiModel)
			if m.pendingDisclosure == nil {
				t.Fatal("tool output was sent without disclosure consent")
			}
			for _, msg := range m.messages {
				if strings.Contains(msg.Content, payload) {
					t.Fatal("payload entered history before approval")
				}
			}
			m.viewport.GotoBottom()
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
			m = updated.(uiModel)
			last := m.messages[len(m.messages)-1]
			if last.Content != payload || last.ToolCallID != tc.ID {
				t.Fatalf("approved payload changed: %+v", last)
			}
		})
	}
}

func TestDisclosureFailsClosedOnDeceptiveCharacters(t *testing.T) {
	_, m := testModel(t)
	tc := testToolCall("list_dir", `{}`)
	m.pendingTools = []ToolCall{tc}
	payload := "safe\u202E" + "danger"
	updated, _ := m.Update(toolExecutedMsg{toolMsg: Message{Role: "tool", ToolCallID: tc.ID, Content: payload}})
	m = updated.(uiModel)
	if m.pendingDisclosure != nil {
		t.Fatal("deceptive output was offered for undisclosed sending")
	}
	for _, msg := range m.messages {
		if strings.Contains(msg.Content, payload) {
			t.Fatal("deceptive payload entered history")
		}
	}
}

func TestDisclosureOfLongOutputRequiresScrollToEnd(t *testing.T) {
	_, m := testModel(t)
	m.viewport.Width, m.viewport.Height = 40, 5
	tc := testToolCall("go_verify", `{}`)
	m.pendingTools = []ToolCall{tc}
	updated, _ := m.Update(toolExecutedMsg{toolMsg: Message{Role: "tool", ToolCallID: tc.ID, Content: strings.Repeat("long-line\n", 500)}})
	m = updated.(uiModel)
	if m.pendingDisclosure == nil || m.viewport.TotalLineCount() <= m.viewport.Height {
		t.Fatal("long output was not rendered as a scrollable review")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(uiModel)
	if m.pendingDisclosure == nil {
		t.Fatal("approval accepted before scrolling to the end")
	}
}

func TestAdvisorySecretDetectionIsExplicitlyLimited(t *testing.T) {
	if len(advisorySecretWarnings("sk-"+strings.Repeat("A", 30))) == 0 {
		t.Fatal("advisory detector missed a common key format")
	}
	if len(advisorySecretWarnings("my novel credential format: zz-1234567890123456")) != 0 {
		t.Fatal("advisory detector falsely claimed comprehensive detection")
	}
}
