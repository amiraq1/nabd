//go:build linux

package tools

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func startEscapedChild(t *testing.T, ctx context.Context, pidFile string, leaderExits bool) (*exec.Cmd, *processTree) {
	t.Helper()
	script := `setsid sh -c 'echo $$ > "$1"; exec sleep 30' sh "$1" &`
	if !leaderExits {
		script += ` wait`
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", script, "sh", pidFile)
	tree := configureProcessTree(cmd)
	if err := tree.Prepare(); err != nil {
		t.Fatalf("prepare process tree: %v", err)
	}
	return cmd, tree
}

func TestProcessTreeKillCatchesSetsidEscape(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	cmd, tree := startEscapedChild(t, ctx, pidFile, false)
	cmd.WaitDelay = 2 * time.Second
	_ = cmd.Run()
	if err := tree.Cleanup(); err != nil {
		t.Fatalf("cleanup escaped process tree: %v", err)
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("expected deadline, got %v", ctx.Err())
	}
	assertProcessGone(t, pidFile)
}

func TestProcessTreeCleanupAfterLeaderExit(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	cmd, tree := startEscapedChild(t, context.Background(), pidFile, true)
	if err := cmd.Run(); err != nil {
		t.Fatalf("leader unexpectedly failed: %v", err)
	}
	if err := tree.Cleanup(); err != nil {
		t.Fatalf("cleanup after leader exit: %v", err)
	}
	assertProcessGone(t, pidFile)
}

func assertProcessGone(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("child pid was not written: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return
		}
		if err != nil && err != syscall.EPERM {
			t.Fatalf("kill(0, %d): %v", pid, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("escaped descendant %d survived tree cleanup", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

const (
	processTreeTestModeEnv      = "NABD_PROCESS_TREE_TEST_MODE"
	processTreeTestPIDEnv       = "NABD_PROCESS_TREE_TEST_PID"
	processTreeTestReadyEnv     = "NABD_PROCESS_TREE_TEST_READY"
	processTreeTestChildModeEnv = "NABD_PROCESS_TREE_TEST_CHILD_MODE"
	processTreeTestChildPIDEnv  = "NABD_PROCESS_TREE_TEST_CHILD_PID"
)

// TestProcessTreeFixture is re-executed as a managed command by the process
// tree tests. Files provide explicit readiness synchronization; no test relies
// on a guessed startup delay.
func TestProcessTreeFixture(t *testing.T) {
	mode := os.Getenv(processTreeTestModeEnv)
	if mode == "" {
		return
	}
	if mode == "setsid" {
		if _, err := unix.Setsid(); err != nil {
			t.Fatalf("setsid: %v", err)
		}
	}
	if mode == "setpgid" {
		if err := syscall.Setpgid(0, 0); err != nil {
			t.Fatalf("setpgid: %v", err)
		}
	}

	pidPath := os.Getenv(processTreeTestPIDEnv)
	if pidPath == "" {
		t.Fatal("fixture PID path is required")
	}
	childMode := os.Getenv(processTreeTestChildModeEnv)
	if childMode != "" {
		childPID := os.Getenv(processTreeTestChildPIDEnv)
		if childPID == "" {
			t.Fatal("fixture child PID path is required")
		}
		child := processTreeFixtureCommand(childMode, childPID, "", "", "")
		if err := child.Start(); err != nil {
			t.Fatalf("start fixture child: %v", err)
		}
		waitForProcessTreeFile(t, childPID)
	}
	writeProcessTreePID(t, pidPath)
	if readyPath := os.Getenv(processTreeTestReadyEnv); readyPath != "" {
		if err := os.WriteFile(readyPath, []byte("ready\n"), 0o600); err != nil {
			t.Fatalf("write fixture readiness: %v", err)
		}
	}
	if mode == "exit" || mode == "orphan" {
		return
	}
	select {}
}

func processTreeFixtureCommand(mode, pidPath, readyPath, childMode, childPIDPath string) *exec.Cmd {
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestProcessTreeFixture$")
	env := withoutProcessTreeTestEnv(os.Environ())
	env = append(env,
		processTreeTestModeEnv+"="+mode,
		processTreeTestPIDEnv+"="+pidPath,
	)
	if readyPath != "" {
		env = append(env, processTreeTestReadyEnv+"="+readyPath)
	}
	if childMode != "" {
		env = append(env,
			processTreeTestChildModeEnv+"="+childMode,
			processTreeTestChildPIDEnv+"="+childPIDPath,
		)
	}
	cmd.Env = env
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd
}

func withoutProcessTreeTestEnv(env []string) []string {
	keys := map[string]struct{}{
		processTreeTestModeEnv: {}, processTreeTestPIDEnv: {}, processTreeTestReadyEnv: {},
		processTreeTestChildModeEnv: {}, processTreeTestChildPIDEnv: {},
	}
	filtered := make([]string, 0, len(env))
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if _, reserved := keys[key]; !reserved {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func writeProcessTreePID(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatalf("write process PID: %v", err)
	}
}

func waitForProcessTreeFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat synchronization file %s: %v", path, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for synchronization file %s", path)
}

func startManagedFixture(t *testing.T, ctx context.Context, mode, childMode string) (*exec.Cmd, *processTree, string, string) {
	t.Helper()
	dir := t.TempDir()
	rootPID := filepath.Join(dir, "root.pid")
	childPID := filepath.Join(dir, "child.pid")
	ready := filepath.Join(dir, "ready")
	cmd := processTreeFixtureCommand(mode, rootPID, ready, childMode, childPID)
	if ctx != nil {
		cmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessTreeFixture$")
		cmd.Env = processTreeFixtureCommand(mode, rootPID, ready, childMode, childPID).Env
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
	}
	tree := configureProcessTree(cmd)
	if err := tree.Prepare(); err != nil {
		t.Fatalf("prepare managed fixture: %v", err)
	}
	if err := cmd.Start(); err != nil {
		_ = tree.Cleanup()
		t.Fatalf("start managed fixture: %v", err)
	}
	waitForProcessTreeFile(t, ready)
	if childMode != "" {
		waitForProcessTreeFile(t, childPID)
	}
	return cmd, tree, rootPID, childPID
}

func readProcessTreePID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read process PID %s: %v", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse process PID %s: %v", path, err)
	}
	return pid
}

func assertProcessTreeStopped(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		info, found, err := readLinuxProcess(pid)
		if err != nil {
			t.Fatalf("inspect process %d: %v", pid, err)
		}
		if !found || info.state == 'Z' || info.state == 'X' {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("process %d remained alive after process-tree cleanup", pid)
}

func assertProcessTreeAlive(t *testing.T, pid int) {
	t.Helper()
	info, found, err := readLinuxProcess(pid)
	if err != nil {
		t.Fatalf("inspect process %d: %v", pid, err)
	}
	if !found || info.state == 'Z' || info.state == 'X' {
		t.Fatalf("process %d is not alive", pid)
	}
}

func stopManagedFixture(t *testing.T, cmd *exec.Cmd, tree *processTree) {
	t.Helper()
	if cmd.Process != nil && cmd.ProcessState == nil {
		if err := tree.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("request managed fixture cancellation: %v", err)
		}
	}
	_ = cmd.Wait()
	if err := tree.Cleanup(); err != nil {
		t.Errorf("cleanup managed fixture: %v", err)
	}
}

func TestProcessTreeKillsNormalChildAndGrandchild(t *testing.T) {
	for _, tc := range []struct {
		name      string
		childMode string
	}{
		{name: "child"},
		{name: "grandchild", childMode: "sleep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, tree, rootPath, childPath := startManagedFixture(t, nil, "sleep", tc.childMode)
			rootPID := readProcessTreePID(t, rootPath)
			var childPID int
			if childPath != "" && tc.childMode != "" {
				childPID = readProcessTreePID(t, childPath)
			}
			stopManagedFixture(t, cmd, tree)
			assertProcessTreeStopped(t, rootPID)
			if childPID != 0 {
				assertProcessTreeStopped(t, childPID)
			}
		})
	}
}

func TestProcessTreeKillsSessionAndProcessGroupEscapes(t *testing.T) {
	for _, mode := range []string{"setsid", "setpgid"} {
		t.Run(mode, func(t *testing.T) {
			cmd, tree, rootPath, _ := startManagedFixture(t, nil, "leader", mode)
			rootPID := readProcessTreePID(t, rootPath)
			childPath := filepath.Join(filepath.Dir(rootPath), "child.pid")
			childPID := readProcessTreePID(t, childPath)
			stopManagedFixture(t, cmd, tree)
			assertProcessTreeStopped(t, rootPID)
			assertProcessTreeStopped(t, childPID)
		})
	}
}

func TestProcessTreeCleansDescendantThatOutlivesLeader(t *testing.T) {
	cmd, tree, rootPath, childPath := startManagedFixture(t, nil, "orphan", "sleep")
	rootPID := readProcessTreePID(t, rootPath)
	childPID := readProcessTreePID(t, childPath)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("managed leader exit: %v", err)
	}
	if err := tree.Cleanup(); err != nil {
		t.Fatalf("cleanup orphaned descendant: %v", err)
	}
	assertProcessTreeStopped(t, rootPID)
	assertProcessTreeStopped(t, childPID)
}

func TestProcessTreeDoesNotKillUnrelatedSibling(t *testing.T) {
	cmd, tree, _, _ := startManagedFixture(t, nil, "sleep", "")
	siblingPath := filepath.Join(t.TempDir(), "sibling.pid")
	sibling := processTreeFixtureCommand("sleep", siblingPath, "", "", "")
	if err := sibling.Start(); err != nil {
		stopManagedFixture(t, cmd, tree)
		t.Fatalf("start unrelated sibling: %v", err)
	}
	waitForProcessTreeFile(t, siblingPath)
	siblingPID := readProcessTreePID(t, siblingPath)

	stopManagedFixture(t, cmd, tree)
	assertProcessTreeAlive(t, siblingPID)
	if err := sibling.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("stop unrelated sibling after assertion: %v", err)
	}
	_ = sibling.Wait()
}

func TestProcessTreeCommandsRunConcurrently(t *testing.T) {
	first, firstTree, _, firstChildPath := startManagedFixture(t, nil, "leader", "sleep")
	second, secondTree, _, secondChildPath := startManagedFixture(t, nil, "leader", "sleep")
	firstChild := readProcessTreePID(t, firstChildPath)
	secondChild := readProcessTreePID(t, secondChildPath)

	stopManagedFixture(t, first, firstTree)
	assertProcessTreeStopped(t, firstChild)
	assertProcessTreeAlive(t, secondChild)
	stopManagedFixture(t, second, secondTree)
	assertProcessTreeStopped(t, secondChild)
}

func TestProcessTreeCancellationDuringStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "root.pid")
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessTreeFixture$")
	cmd.Env = processTreeFixtureCommand("sleep", pidPath, "", "", "").Env
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	tree := configureProcessTree(cmd)
	if err := tree.Prepare(); err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		_ = tree.Cleanup()
		t.Fatalf("start helper for startup cancellation: %v", err)
	}
	cancel()
	_ = cmd.Wait()
	if err := tree.Cleanup(); err != nil {
		t.Fatalf("cleanup startup cancellation: %v", err)
	}
	if _, err := os.Stat(pidPath); err == nil {
		assertProcessTreeStopped(t, readProcessTreePID(t, pidPath))
	} else if !os.IsNotExist(err) {
		t.Fatalf("check startup fixture: %v", err)
	}
}

func TestProcessTreeCleanupAfterNormalCompletion(t *testing.T) {
	cmd, tree, rootPath, _ := startManagedFixture(t, nil, "exit", "")
	rootPID := readProcessTreePID(t, rootPath)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("normal managed completion: %v", err)
	}
	if err := tree.Cleanup(); err != nil {
		t.Fatalf("cleanup after normal completion: %v", err)
	}
	assertProcessTreeStopped(t, rootPID)
}

func TestProcessTreeForcedCancellationCleansAllOwnedDescendants(t *testing.T) {
	cmd, tree, rootPath, childPath := startManagedFixture(t, nil, "leader", "sleep")
	rootPID := readProcessTreePID(t, rootPath)
	childPID := readProcessTreePID(t, childPath)
	stopManagedFixture(t, cmd, tree)
	assertProcessTreeStopped(t, rootPID)
	assertProcessTreeStopped(t, childPID)
}
