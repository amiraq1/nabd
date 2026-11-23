//go:build !linux

package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

type mcpProcess struct{}

//lint:ignore U1000 paired with the Linux implementation
func startMCPProcess(context.Context, string, string, string) (*mcpProcess, *os.File, *os.File, error) {
	return nil, nil, nil, fmt.Errorf("MCP process isolation unsupported on this platform")
}
func startMCPProcessWithCmd(context.Context, *exec.Cmd) (*mcpProcess, *os.File, *os.File, error) {
	return nil, nil, nil, fmt.Errorf("MCP process isolation unsupported on this platform")
}
func (*mcpProcess) terminate()          {}
func (*mcpProcess) closeAndWait() error { return nil }
