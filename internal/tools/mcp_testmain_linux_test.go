//go:build linux && (amd64 || arm64)

package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "nabd-mcp-guard-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	guard := filepath.Join(dir, "nabd-mcp-guard")
	if output, err := exec.Command("go", "build", "-o", guard, "../../cmd/mcp-guard").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "guard build failed: %v\n%s", err, output)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	mcpGuardOverride = guard
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
