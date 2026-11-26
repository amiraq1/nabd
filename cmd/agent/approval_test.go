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
	t.Cleanup(func() { _ = reg.Close() })
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

func TestWebFetchApprovalShowsFullQueryAndBindsCanonicalURL(t *testing.T) {
	t.Setenv(tools.WebSearchProviderEnv, "brave")
	t.Setenv(tools.WebBraveAPIKeyEnv, "brave-test-key")

	dir := t.TempDir()
	reg, err := tools.NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	// 1. Query string parameters must be shown unclipped in preview so secret exfiltration cannot hide.
	args := `{"url":"HTTPS://Docs.Example.COM/collect?secret=EXFIL_KEY_999&step=2"}`
	tc := ToolCall{ID: "call_wf_q", Type: "function"}
	tc.Function.Name = "web_fetch"
	tc.Function.Arguments = args
	preview, err := prepareApprovalPreview(reg, tc)
	if err != nil {
		t.Fatalf("prepareApprovalPreview valid web_fetch failed: %v", err)
	}
	if !strings.Contains(preview.Summary, "secret=EXFIL_KEY_999&step=2") {
		t.Fatalf("preview omitted query string: %s", preview.Summary)
	}
	if !strings.Contains(preview.Summary, "https://docs.example.com/collect?secret=EXFIL_KEY_999&step=2") {
		t.Fatalf("preview omitted normalized canonical URL: %s", preview.Summary)
	}
	if strings.Contains(preview.Summary, "omitted") {
		t.Fatalf("preview still claims parameters are omitted: %s", preview.Summary)
	}

	// 2. Approval token bound to canonical identity cannot be used with a mismatched URL/identity,
	//    and audit.log logs ONLY host=docs.example.com without query parameters.
	app, err := reg.ApproveWithIdentity("web_fetch", []byte(args), preview.IdentitySHA256)
	if err != nil {
		t.Fatalf("ApproveWithIdentity web_fetch: %v", err)
	}
	tamperedArgs := []byte(`{"url":"https://docs.example.com/collect?secret=OTHER&step=2"}`)
	if out := reg.RunApproved(t.Context(), "web_fetch", tamperedArgs, app); !strings.Contains(out, "invalid or expired") {
		t.Fatalf("RunApproved accepted tampered web_fetch args for approved token: %s", out)
	}
	if _, err := reg.ApproveWithIdentity("web_fetch", []byte(args), "webfetch:sha256:wrong"); err == nil {
		t.Fatal("ApproveWithIdentity accepted wrong canonical URL identity")
	}
	auditBytes, err := os.ReadFile(filepath.Join(dir, ".nabd", "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(auditBytes), "EXFIL_KEY_999") || !strings.Contains(string(auditBytes), "host=docs.example.com") {
		t.Fatalf("audit.log leaked query string or omitted normalized host: %s", string(auditBytes))
	}

	// 3. Reject hidden/unsafe/private/unknown-field/control-char web_fetch inputs before asking consent.
	badCases := []struct {
		name string
		args string
	}{
		{"bidi_override_query", `{"url":"https://docs.example.com/p?x=\u202espoof"}`},
		{"localhost_ssrf", `{"url":"https://localhost/admin"}`},
		{"unknown_json_field", `{"url":"https://docs.example.com/p","extra":1}`},
		{"url_over_2048", `{"url":"https://docs.example.com/` + strings.Repeat("a", 2060) + `"}`},
	}
	for _, tcCase := range badCases {
		t.Run(tcCase.name, func(t *testing.T) {
			badCall := ToolCall{ID: "bad_" + tcCase.name, Type: "function"}
			badCall.Function.Name = "web_fetch"
			badCall.Function.Arguments = tcCase.args
			if _, err := prepareApprovalPreview(reg, badCall); err == nil {
				t.Fatalf("expected prepareApprovalPreview to reject %s", tcCase.name)
			}
		})
	}
}
