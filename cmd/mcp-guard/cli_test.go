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
	builtGuardOnce sync.Once
	builtGuardPath string
	builtGuardErr  error
)

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
