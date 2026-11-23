//go:build !linux

package main

import (
	"fmt"
	"runtime"
)

func installFilter() error {
	return fmt.Errorf("MCP process isolation is unsupported on %s", runtime.GOOS)
}

func execTarget(string, []string) error {
	return fmt.Errorf("MCP process isolation is unsupported")
}
