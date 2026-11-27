//go:build !unix && !windows

package audit

import (
	"errors"
	"os"
	"time"
)

var errLockBusy = errors.New("audit lock is held by another process")

type auditLock struct{}

func acquireAuditLock(*os.Root, time.Duration) (*auditLock, error) {
	return nil, errors.New("audit: cross-process audit locking is unsupported on this platform")
}

func (*auditLock) close() error { return nil }
