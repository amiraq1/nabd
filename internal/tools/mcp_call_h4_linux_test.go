//go:build linux

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const mcpCallH4FixtureEnv = "NABD_MCP_CALL_H4_FIXTURE"

// TestMCPCallH4ServerFixture is re-executed as the stdio MCP server by the
// cancellation regression test. The marker keeps the fixture inert in the
// ordinary package test run.
func TestMCPCallH4ServerFixture(t *testing.T) {
	if os.Getenv(mcpCallH4FixtureEnv) != "1" {
		return
	}

	s := server.NewMCPServer("mcp_call H4 fixture", "1.0.0")
	s.AddTool(mcp.NewTool("wait", mcp.WithString("pid_file")), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		pidFile, ok := request.GetArguments()["pid_file"].(string)
		if !ok || pidFile == "" {
			return nil, fmt.Errorf("pid_file is required")
		}

		child := exec.Command("sh", "-c", `exec setsid sh -c 'echo $$ > "$1"; exec sleep 60' sh "$1"`, "sh", pidFile)
		child.Stdout = io.Discard
		child.Stderr = io.Discard
		if err := child.Start(); err != nil {
			return nil, fmt.Errorf("start fixture descendant: %w", err)
		}

		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(pidFile); err == nil {
				break
			} else if !os.IsNotExist(err) {
				return nil, fmt.Errorf("wait for fixture descendant pid: %w", err)
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("timed out waiting for fixture descendant pid")
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		}

		<-ctx.Done()
		return nil, ctx.Err()
	})

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func TestMCPCallH4CancellationKillsManagedTreeNotIndependentProcess(t *testing.T) {
	independent := exec.Command("sleep", "60")
	if err := independent.Start(); err != nil {
		t.Fatalf("start independent sibling: %v", err)
	}
	defer func() {
		if independent.Process != nil {
			_ = independent.Process.Kill()
			_ = independent.Wait()
		}
	}()

	fixtureBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "mcp-fixture")
	script := fmt.Sprintf("#!/bin/sh\nexport %s=1\nexec '%s' -test.run='^TestMCPCallH4ServerFixture$'\n", mcpCallH4FixtureEnv, strings.ReplaceAll(fixtureBinary, "'", "'\\''"))
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write MCP fixture wrapper: %v", err)
	}

	reg, err := NewRegistry(t.TempDir(), func(string, string) bool { return true })
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.Close()

	pidFile := filepath.Join(t.TempDir(), "managed-child.pid")
	args, err := json.Marshal(map[string]any{
		"command": wrapper,
		"tool":    "wait",
		"arguments": map[string]any{
			"pid_file": pidFile,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan string, 1)
	go func() {
		done <- reg.Run(ctx, "mcp_call", args)
	}()

	waitForProcessTreeFile(t, pidFile)
	managedChildPID := readProcessTreePID(t, pidFile)
	assertProcessTreeAlive(t, managedChildPID)
	assertProcessTreeAlive(t, independent.Process.Pid)

	cancel()
	var result string
	select {
	case result = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Registry.Run did not return after parent context cancellation")
	}
	if !strings.Contains(result, "context canceled") {
		t.Fatalf("Registry.Run result = %q, want canceled MCP call", result)
	}

	assertProcessTreeStopped(t, managedChildPID)
	assertProcessTreeAlive(t, independent.Process.Pid)
}
