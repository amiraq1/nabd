package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var (
	builtGuardOnce  sync.Once
	builtGuardPath  string
	builtGuardErr   error
	builtTargetOnce sync.Once
	builtTargetPath string
	builtTargetErr  error
)

func getTestTargetBinary(t *testing.T) string {
	t.Helper()
	builtTargetOnce.Do(func() {
		binName := "test-mcp-target"
		if runtime.GOOS == "windows" {
			binName += ".exe"
		}
		dir, err := os.MkdirTemp("", "guard-target-*")
		if err != nil {
			builtTargetErr = err
			return
		}
		src := filepath.Join(dir, "main.go")
		code := `package main
import (
	"fmt"
	"os"
	"strconv"
	"strings"
)
func main() {
	if len(os.Args) >= 3 && os.Args[1] == "--exit" {
		code, _ := strconv.Atoi(os.Args[2])
		os.Exit(code)
	}
	fmt.Println(strings.Join(os.Args[1:], " "))
}
`
		if err := os.WriteFile(src, []byte(code), 0o600); err != nil {
			builtTargetErr = err
			return
		}
		dest := filepath.Join(dir, binName)
		cmd := exec.Command("go", "build", "-trimpath", "-o", dest, src)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			builtTargetErr = fmt.Errorf("build target: %w\n%s", err, string(out))
			return
		}
		builtTargetPath = dest
	})
	if builtTargetErr != nil {
		t.Fatalf("failed to build test target binary: %v", builtTargetErr)
	}
	return builtTargetPath
}

func getGuardBinary(t *testing.T) string {
	t.Helper()
	builtGuardOnce.Do(func() {
		binName := "test-mcp-guard"
		if runtime.GOOS == "windows" {
			binName += ".exe"
		}
		dir, err := os.MkdirTemp("", "guard-bin-*")
		if err != nil {
			builtGuardErr = err
			return
		}
		dest := filepath.Join(dir, binName)
		cmd := exec.Command("go", "build", "-trimpath", "-o", dest, ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			builtGuardErr = fmt.Errorf("build guard: %w\n%s", err, string(out))
			return
		}
		builtGuardPath = dest
	})
	if builtGuardErr != nil {
		t.Fatalf("failed to build mcp-guard binary: %v", builtGuardErr)
	}
	return builtGuardPath
}

func TestCLIMissingArgs(t *testing.T) {
	guard := getGuardBinary(t)
	cmd := exec.Command(guard)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected failure when running mcp-guard with no arguments, but got exit 0")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("expected exit code 2 on missing arguments, got %d", exitErr.ExitCode())
	}
	if !strings.Contains(string(out), "usage: mcp-guard <command>") {
		t.Errorf("expected usage message in output, got: %q", string(out))
	}
}

func TestCLINonexistentTarget(t *testing.T) {
	guard := getGuardBinary(t)
	nonexistent := filepath.Join(t.TempDir(), "nonexistent-cmd-xyz-987")
	cmd := exec.Command(guard, nonexistent)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected failure on nonexistent target, but got exit 0")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("expected exit code 1 on nonexistent target, got %d", exitErr.ExitCode())
	}
	if !strings.Contains(string(out), "mcp-guard:") {
		t.Errorf("expected 'mcp-guard:' in stderr output, got: %q", string(out))
	}
}

func TestCLIUnexecutableTarget(t *testing.T) {
	guard := getGuardBinary(t)
	dir := t.TempDir()
	nonExec := filepath.Join(dir, "not-executable.txt")
	if err := os.WriteFile(nonExec, []byte("plain text"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(guard, nonExec)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected failure on unexecutable target, but got exit 0")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("expected exit code 1 on unexecutable target, got %d", exitErr.ExitCode())
	}
	if !strings.Contains(string(out), "mcp-guard:") {
		t.Errorf("expected 'mcp-guard:' in stderr output, got: %q", string(out))
	}
}

func TestCLIFailClosedNoUnguardedExecution(t *testing.T) {
	guard := getGuardBinary(t)
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "sentinel.txt")

	var targetScript string
	if runtime.GOOS == "windows" {
		targetScript = filepath.Join(dir, "probe.bat")
		_ = os.WriteFile(targetScript, []byte("@echo executed > "+sentinel+"\r\n"), 0600)
	} else {
		targetScript = filepath.Join(dir, "probe.sh")
		_ = os.WriteFile(targetScript, []byte("#!/bin/sh\ntouch "+sentinel+"\n"), 0600)
	}

	cmd := exec.Command(guard, targetScript)
	_, _ = cmd.CombinedOutput()

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("sentinel file was created: unguarded target command executed when it should have failed closed!")
	}
}
