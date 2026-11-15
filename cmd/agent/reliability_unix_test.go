//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The UI must propagate quit to the real go_verify process group, and the
// command must finish before the registry can safely be closed.
func TestReliabilityQuitCancelsToolProcessTree(t *testing.T) {
	dir, m := testModel(t)
	bin := t.TempDir()
	pidFile := filepath.Join(dir, "pids")
	script := "#!/bin/sh\nsleep 30 &\necho $$ $! > " + strconv.Quote(pidFile) + "\nwait\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	tc := testToolCall("go_verify", `{"pkg":"./..."}`)
	m.pendingTools = []ToolCall{tc}
	m.awaitingApproval = true
	m.busy = true
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("missing tool command")
	}
	m = updated.(uiModel)
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var fields []string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(pidFile)
		fields = strings.Fields(string(b))
		if len(fields) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(fields) != 2 {
		t.Fatal("tool did not start")
	}
	parent, _ := strconv.Atoi(fields[0])
	child, _ := strconv.Atoi(fields[1])
	defer syscall.Kill(-parent, syscall.SIGKILL)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case result := <-done:
		msg, ok := result.(toolExecutedMsg)
		if !ok || !strings.Contains(msg.toolMsg.Content, "CANCELLED (context canceled)") {
			t.Fatal("user cancellation was not distinguished from failure")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tool command did not finish after quit")
	}
	if !m.lifecycle.wait(time.Second) {
		t.Fatal("shutdown did not finish active tool")
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(child, 0)
		if err == syscall.ESRCH {
			return
		}
		if b, e := os.ReadFile("/proc/" + strconv.Itoa(child) + "/stat"); e == nil {
			parts := strings.Fields(string(b))
			if len(parts) > 2 && parts[2] == "Z" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child process remained alive after quit")
}
