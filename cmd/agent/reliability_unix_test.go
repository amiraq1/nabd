//go:build unix

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/amiraq1/nabd/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

func runUnixProcessTreeForTest(t *testing.T, script string) func(context.Context, *tools.Registry, ToolCall, *tools.Approval) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatalf("sh not found: %v", err)
	}
	return func(ctx context.Context, _ *tools.Registry, _ ToolCall, _ *tools.Approval) string {
		cmd := exec.CommandContext(ctx, sh, "-c", script)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return nil
			}
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		cmd.WaitDelay = 2 * time.Second
		if err := cmd.Run(); err != nil {
			if ctx.Err() == context.Canceled {
				return "CANCELLED (context canceled)"
			}
			return "Error: " + err.Error()
		}
		return "PASS"
	}
}

// The UI must propagate quit to the active tool process group, and the
// command must finish before the registry can safely be closed.
func TestReliabilityQuitCancelsToolProcessTree(t *testing.T) {
	dir, m := testModel(t)
	pidFile := filepath.Join(dir, "pids")
	script := "sleep 30 &\necho $$ $! > " + strconv.Quote(pidFile) + "\nwait\n"
	m.runToolForTest = runUnixProcessTreeForTest(t, script)
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
