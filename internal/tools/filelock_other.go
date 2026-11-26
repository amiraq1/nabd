//go:build !windows && !linux && !darwin && !android

package tools

import (
	"fmt"
	"os"
)

type processLock struct{}

func acquireProcessLock(*os.Root) (*processLock, error) {
	return nil, fmt.Errorf("cross-process edit locking is unsupported on this platform")
}

func (*processLock) close() error { return nil }
