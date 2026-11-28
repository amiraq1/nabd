//go:build !unix && !windows

package memory

import (
	"context"
	"os"
	"time"
)

type memoryDirHandle struct{}

func (h *memoryDirHandle) close() {}

type memoryLock struct{}

func (l *memoryLock) close() error { return nil }

func directorySyncSupported() bool { return false }

func syncDirectoryFD(*os.File) error { return nil }

func openMemoryDir(*os.Root, bool) (*memoryDirHandle, error) {
	return nil, ErrUnsupportedPlatform
}

func acquireMemoryLock(context.Context, *memoryDirHandle, time.Duration) (*memoryLock, error) {
	return nil, ErrUnsupportedPlatform
}

func readEntriesFile(*memoryDirHandle, int64) ([]byte, error) {
	return nil, ErrUnsupportedPlatform
}

func writeEntriesAtomic(*memoryDirHandle, []byte, func(*os.File) error, func(), func(*os.File, []byte) error) error {
	return ErrUnsupportedPlatform
}
