//go:build !windows && !linux && !darwin && !android

package tools

import "fmt"

type processLock struct{}

func acquireProcessLock(string) (*processLock, error) {
	return nil, fmt.Errorf("cross-process edit locking is unsupported on this platform")
}

func (*processLock) close() error { return nil }
