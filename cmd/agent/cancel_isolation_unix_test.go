//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/amiraq1/nabd/internal/audit"
	tea "github.com/charmbracelet/bubbletea"
)

func TestEscCancelsRunningToolContextAndDropsLateToolExecutedMsg(t *testing.T) {
	dir, m := testModel(t)
	bin := t.TempDir()
	startedFile := filepath.Join(dir, "started")
	script := "#!/bin/sh\necho $$ > " + strconv.Quote(startedFile) + "\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	t1 := testToolCall("go_verify", `{"pkg":"./..."}`)
	t2 := testToolCall("read_file", `{"path":"a.txt"}`)
	m.pendingTools = []ToolCall{t1, t2}
	m.awaitingApproval = true
	m.busy = true
	oldGen := m.taskGen

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("expected tool execution cmd")
	}
	m = updated.(uiModel)

	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(startedFile); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(startedFile); err != nil {
		t.Fatal("running tool process did not start")
	}

	// Press Esc while tool #1 is actively running in multi-tool batch [t1, t2].
	updated, escCmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if escCmd != nil {
		t.Fatal("Esc cancel should not launch next tool or LLM")
	}
	m = updated.(uiModel)
	if m.busy || len(m.pendingTools) != 0 || m.taskGen != oldGen+1 {
		t.Fatalf("Esc did not cancel active tool state cleanly")
	}

	var lateMsg tea.Msg
	select {
	case lateMsg = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Esc did not cancel running tool process tree")
	}

	msgCount := len(m.messages)
	updated, nextCmd := m.Update(lateMsg)
	m = updated.(uiModel)
	if nextCmd != nil || m.pendingDisclosure != nil || len(m.messages) != msgCount {
		t.Fatalf("late cancelled toolExecutedMsg polluted UI state: disclosure=%v msgs=%d->%d",
			m.pendingDisclosure, msgCount, len(m.messages))
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	alog, err := audit.Open(root)
	if err != nil {
		t.Fatalf("audit.Open failed after cancel: %v", err)
	}
	if err := alog.Verify(); err != nil {
		t.Fatalf("audit.Verify failed after cancel: %v", err)
	}
}
