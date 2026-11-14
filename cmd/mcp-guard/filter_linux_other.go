//go:build linux && !arm64 && !amd64

package main

import (
	"fmt"
	"runtime"
)

func installFilter() error {
	return fmt.Errorf("MCP process isolation is unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
}

func execTarget(string) error {
	return fmt.Errorf("MCP process isolation is unsupported")
}
