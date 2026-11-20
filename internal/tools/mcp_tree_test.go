package tools

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// buildTreeHelper builds a helper binary that spawns a grandchild and exposes both PIDs.
func buildTreeHelper(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "helper_main.go")
	exeName := "tree_helper"
	if runtime.GOOS == "windows" {
		exeName = "tree_helper.exe"
	}
	bin := filepath.Join(dir, exeName)

	code := `package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "grandchild" {
		time.Sleep(60 * time.Second)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	gc := exec.Command(exe, "grandchild")
	if err := gc.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "gc start err: %v\n", err)
		os.Exit(1)
	}
	// Print grandchild PID to stdout
	fmt.Printf("GRANDCHILD_PID:%d\n", gc.Process.Pid)
	os.Stdout.Sync()
	time.Sleep(60 * time.Second)
}
`
	if err := os.WriteFile(src, []byte(code), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build tree helper: %v\n%s", err, string(out))
	}
	return bin
}

// TestProcessTreeCancellation verifies that process-group cancellation terminates
// both the child and grandchild processes.
func TestProcessTreeCancellation(t *testing.T) {
	helperBin := buildTreeHelper(t)

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, helperBin)
	configureProcessTree(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start helper: %v", err)
	}

	childPID := cmd.Process.Pid
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("failed to read grandchild pid: %v", err)
	}

	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "GRANDCHILD_PID:") {
		_ = cmd.Process.Kill()
		t.Fatalf("unexpected output: %s", line)
	}
	gcPIDStr := strings.TrimPrefix(line, "GRANDCHILD_PID:")
	gcPID, err := strconv.Atoi(gcPIDStr)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("invalid gc pid %q: %v", gcPIDStr, err)
	}

	// Verify both are currently alive
	if !isProcessAlive(childPID) {
		t.Fatalf("child process %d is not alive", childPID)
	}
	if !isProcessAlive(gcPID) {
		t.Fatalf("grandchild process %d is not alive", gcPID)
	}

	// Trigger cancellation via context
	cancel()
	_ = cmd.Wait()

	// Wait up to 3 seconds for OS to reap descendants
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !isProcessAlive(childPID) && !isProcessAlive(gcPID) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if isProcessAlive(childPID) {
		t.Errorf("child process %d is still alive after cancellation", childPID)
	}
	if isProcessAlive(gcPID) {
		t.Errorf("grandchild process %d is still alive after cancellation", gcPID)
	}
}

// TestRegistryCloseTerminatesProcessTree verifies that Registry.Close() terminates
// all child and grandchild processes started by the registry.
func TestRegistryCloseTerminatesProcessTree(t *testing.T) {
	helperBin := buildTreeHelper(t)
	dir := t.TempDir()
	reg, err := NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Launch helper bound to reg.ctx
	cmd := exec.CommandContext(reg.ctx, helperBin)
	configureProcessTree(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	childPID := cmd.Process.Pid
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	gcPIDStr := strings.TrimPrefix(strings.TrimSpace(line), "GRANDCHILD_PID:")
	gcPID, _ := strconv.Atoi(gcPIDStr)

	// Close registry - should trigger cancelCtx and terminate process tree
	_ = reg.Close()
	_ = cmd.Wait()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !isProcessAlive(childPID) && !isProcessAlive(gcPID) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if isProcessAlive(childPID) {
		t.Errorf("child process %d survived reg.Close()", childPID)
	}
	if isProcessAlive(gcPID) {
		t.Errorf("grandchild process %d survived reg.Close()", gcPID)
	}

	// Repeated Close must be safe and idempotent
	if err := reg.Close(); err != nil {
		t.Errorf("repeated Close() returned error: %v", err)
	}
}
