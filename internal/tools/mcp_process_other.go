//go:build !linux

package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

type mcpProcess struct{}

func startMCPProcessWithCmd(context.Context, *exec.Cmd) (*mcpProcess, *os.File, *os.File, error) {
	return nil, nil, nil, fmt.Errorf("MCP process isolation unsupported on this platform")
}
func (*mcpProcess) terminate()          {}
func (*mcpProcess) closeAndWait() error { return nil }
