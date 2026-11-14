//go:build linux

package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "nabd-guard-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "guard temp dir:", err)
		os.Exit(1)
	}
	guard := filepath.Join(dir, "nabd-mcp-guard")
	cmd := exec.Command("go", "build", "-o", guard, "../../cmd/mcp-guard")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build mcp-guard: %v\n%s", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	if err := os.Setenv("NABD_MCP_GUARD", guard); err != nil {
		fmt.Fprintln(os.Stderr, "set NABD_MCP_GUARD:", err)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
