package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amiraq1/nabd/internal/audit"
	tea "github.com/charmbracelet/bubbletea"
)

func TestIntegrationUserRejectionIsAudited(t *testing.T) {
	dir, m := testModel(t)
	tc := testToolCall("mcp_call", `{"command":"example","tool":"test","arguments":{"token":"SYNTHETIC_PRIVATE_MARKER"}}`)
	m.pendingTools = []ToolCall{tc}
	next, _ := m.advance()
	m = next.(uiModel)
	if !m.awaitingApproval {
		t.Fatal("approval was not shown")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(uiModel)
	data, err := os.ReadFile(filepath.Join(dir, ".nabd", "audit.log"))
	if err != nil {
		t.Fatal("no audit record for rejection")
	}
	if strings.Contains(string(data), "SYNTHETIC_PRIVATE_MARKER") {
		t.Fatal("audit leaked argument content")
	}
	denied := false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e audit.Event
		if json.Unmarshal([]byte(line), &e) != nil {
			t.Fatal("invalid audit event")
		}
		if e.Event == "denied" && e.Tool == "mcp_call" && e.ArgsSHA256 != "" {
			denied = true
		}
	}
	if !denied {
		t.Fatal("denial event missing")
	}
	if len(m.pendingTools) != 0 {
		t.Fatal("rejected tool remains pending")
	}
}
func TestIntegrationUserRejectionWarnsIfAuditFails(t *testing.T) {
	dir, m := testModel(t)
	if err := os.WriteFile(filepath.Join(dir, ".nabd"), []byte("blocking file"), 0600); err != nil {
		t.Fatal(err)
	}
	tc := testToolCall("mcp_call", `{"command":"example","tool":"test"}`)
	m.pendingTools = []ToolCall{tc}
	next, _ := m.advance()
	m = next.(uiModel)
	if !m.awaitingApproval {
		t.Fatal("approval was not shown")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(uiModel)
	if len(m.messages) == 0 || !strings.Contains(strings.ToLower(m.messages[len(m.messages)-1].Display), "audit") {
		t.Fatal("audit failure was not surfaced")
	}
	if len(m.pendingTools) != 0 {
		t.Fatal("rejected tool remains pending")
	}
}
