package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amiraq1/nabd/internal/audit"
	"github.com/amiraq1/nabd/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

func TestEscCancelsInFlightLLMAndDropsStaleToolAndLLMMessages(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	started := make(chan struct{})
	canceled := make(chan struct{})
	block := make(chan struct{})
	client := &mockClient{started: started, canceled: canceled, block: block}

	m := initialModel(reg, client)
	m.busy = true
	t1 := ToolCall{ID: "call_1"}
	t1.Function.Name = "read_file"
	t1.Function.Arguments = `{"path":"a.txt"}`
	t2 := ToolCall{ID: "call_2"}
	t2.Function.Name = "edit_file"
	t2.Function.Arguments = `{"path":"a.txt","old_text":"hello","new_text":"world"}`
	m.pendingTools = []ToolCall{t1, t2}

	nextModel, cmd := m.startLLM()
	m = nextModel.(uiModel)
	if cmd == nil {
		t.Fatal("expected LLM command")
	}
	msgCh := make(chan tea.Msg, 1)
	go func() { msgCh <- cmd() }()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(block)
		t.Fatal("mock LLM request did not start")
	}

	// Press Esc while LLM + pending multi-tool queue are active.
	beforeGen := m.taskGen
	updated, escCmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if escCmd != nil {
		t.Fatal("expected nil cmd on Esc cancel")
	}
	m = updated.(uiModel)
	if m.busy || len(m.pendingTools) != 0 || m.awaitingApproval || m.pendingDisclosure != nil {
		t.Fatalf("expected active state cleared after Esc cancel, got busy=%v pending=%d awaiting=%v disc=%v",
			m.busy, len(m.pendingTools), m.awaitingApproval, m.pendingDisclosure)
	}
	if m.taskGen != beforeGen+1 {
		t.Fatalf("expected taskGen incremented from %d, got %d", beforeGen, m.taskGen)
	}

	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		close(block)
		t.Fatal("in-flight LLM context was not cancelled by Esc")
	}
	var lateMsg tea.Msg
	select {
	case lateMsg = <-msgCh:
	case <-time.After(2 * time.Second):
		close(block)
		t.Fatal("LLM goroutine did not finish")
	}

	msgCountAfterEsc := len(m.messages)
	updated, lateCmd := m.Update(lateMsg)
	m = updated.(uiModel)
	if lateCmd != nil || len(m.messages) != msgCountAfterEsc {
		t.Fatalf("stale error/LLM message after cancel mutated UI state")
	}

	// Stale toolExecutedMsg, llmResponseMsg, and errorMsg from the cancelled generation must be dropped.
	updated, _ = m.Update(toolExecutedMsg{gen: beforeGen, toolMsg: Message{Role: "tool", ToolCallID: t1.ID, Content: "stale-secret"}})
	m = updated.(uiModel)
	updated, _ = m.Update(llmResponseMsg{gen: beforeGen, assistantMsg: Message{Role: "assistant", Content: "stale"}})
	m = updated.(uiModel)
	updated, _ = m.Update(errorMsg{gen: beforeGen, err: context.Canceled})
	m = updated.(uiModel)
	if m.pendingDisclosure != nil || len(m.messages) != msgCountAfterEsc || m.busy {
		t.Fatalf("stale generation messages mutated state after cancel")
	}

	// Audit log chain remains valid cleanly after partial operations and cancellation.
	if err := reg.LogDisclosure("read_file", "00", "", false); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	alog, err := audit.Open(root)
	if err != nil {
		t.Fatalf("audit log corrupted after cancel: %v", err)
	}
	if err := alog.Verify(); err != nil {
		t.Fatalf("audit.Verify failed after cancel: %v", err)
	}
}

func TestClearResetsDisclosurePendingToolsAndGeneration(t *testing.T) {
	_, m := testModel(t)
	cancelled := false
	m.requestCancel = func() { cancelled = true }
	m.busy = true
	oldGen := m.taskGen

	tc := testToolCall("read_file", `{"path":"a.txt"}`)
	m.pendingTools = []ToolCall{tc}
	m.awaitingApproval = true
	m.approval = approvalPreview{Summary: "old"}
	m.pendingDisclosure = &Message{Role: "tool", ToolCallID: tc.ID, Content: "sensitive"}

	updated, _ := m.executeLocalCommand("/clear")
	m = updated.(uiModel)
	if !cancelled {
		t.Fatal("/clear did not cancel active request")
	}
	if m.busy || m.awaitingApproval || m.pendingDisclosure != nil || len(m.pendingTools) != 0 {
		t.Fatalf("/clear leaked pending state: busy=%v awaiting=%v disc=%v pending=%d",
			m.busy, m.awaitingApproval, m.pendingDisclosure, len(m.pendingTools))
	}
	if m.taskGen != oldGen+1 || m.status != "Ready" {
		t.Fatalf("expected bumped generation & Ready status, got gen=%d status=%q", m.taskGen, m.status)
	}

	// Late toolExecutedMsg from before /clear must be ignored without panicking.
	updated, _ = m.Update(toolExecutedMsg{gen: oldGen, toolMsg: Message{Role: "tool", ToolCallID: tc.ID, Content: "\x1b[31mANSI"}})
	m = updated.(uiModel)
	if m.pendingDisclosure != nil {
		t.Fatal("stale toolExecutedMsg resurrected disclosure after /clear")
	}

	// Even with matching generation, empty pendingTools must never panic on ANSI or disclosure.
	updated, _ = m.Update(toolExecutedMsg{gen: m.taskGen, toolMsg: Message{Role: "tool", ToolCallID: "orphan", Content: "\x1b[31mANSI"}})
	m = updated.(uiModel)
	if m.pendingDisclosure != nil {
		t.Fatal("orphan toolExecutedMsg created disclosure when pendingTools is empty")
	}
}

func TestEscAtApprovalCancelsBatchAndRecordsDenial(t *testing.T) {
	dir, m := testModel(t)
	t1 := testToolCall("edit_file", `{"path":"a.txt","old_text":"x","new_text":"y"}`)
	t2 := testToolCall("edit_file", `{"path":"b.txt","old_text":"x","new_text":"y"}`)
	m.pendingTools = []ToolCall{t1, t2}
	m.awaitingApproval = true
	m.busy = true
	oldGen := m.taskGen

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(uiModel)
	if cmd != nil {
		t.Fatal("Esc at approval should cancel cleanly without running next tool/LLM")
	}
	if m.busy || m.awaitingApproval || len(m.pendingTools) != 0 || m.taskGen != oldGen+1 {
		t.Fatalf("Esc at approval did not cancel multi-tool queue: busy=%v awaiting=%v pending=%d gen=%d",
			m.busy, m.awaitingApproval, len(m.pendingTools), m.taskGen)
	}

	// Double cancel (/clear after Esc) must remain safe.
	updated, _ = m.executeLocalCommand("/clear")
	m = updated.(uiModel)
	if m.busy || m.pendingDisclosure != nil || len(m.pendingTools) != 0 {
		t.Fatal("double cancel via Esc then /clear left inconsistent state")
	}

	auditData, err := os.ReadFile(filepath.Join(dir, ".nabd", "audit.log"))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if !strings.Contains(string(auditData), `"event":"denied"`) {
		t.Fatalf("expected denied audit record on Esc at approval: %s", string(auditData))
	}
}
