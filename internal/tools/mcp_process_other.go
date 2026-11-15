//go:build !linux

package tools

import (
	"context"
	"fmt"
	"os"
)

type mcpProcess struct{}

func startMCPProcess(context.Context, string, string, string) (*mcpProcess, *os.File, *os.File, error) {
	return nil, nil, nil, fmt.Errorf("MCP process isolation unsupported on this platform")
}
func (*mcpProcess) terminate()          {}
func (*mcpProcess) closeAndWait() error { return nil }
