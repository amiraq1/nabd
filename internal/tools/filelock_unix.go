//go:build linux || darwin || android

package tools

import (
	"errors"
	"fmt"
	"os"

	"github.com/amiraq1/nabd/internal/safefs"
	"golang.org/x/sys/unix"
)

type processLock struct {
	file *os.File
}

func openOrCreateLockedNabdDir(root *os.Root) (*os.File, error) {
	if root == nil {
		return nil, errors.New("nil workspace root")
	}
	rootDir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer rootDir.Close()

	dir, err := safefs.OpenDirAtNoFollow(int(rootDir.Fd()), ".nabd")
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("open .nabd lock directory: %w", err)
		}
		if mkErr := unix.Mkdirat(int(rootDir.Fd()), ".nabd", 0o700); mkErr != nil && !errors.Is(mkErr, unix.EEXIST) {
			return nil, fmt.Errorf("create .nabd lock directory: %w", mkErr)
		}
		dir, err = safefs.OpenDirAtNoFollow(int(rootDir.Fd()), ".nabd")
		if err != nil {
			return nil, fmt.Errorf("open .nabd lock directory: %w", err)
		}
	}
	return dir, nil
}

func acquireProcessLock(root *os.Root) (*processLock, error) {
	nabdDir, err := openOrCreateLockedNabdDir(root)
	if err != nil {
		return nil, err
	}
	defer nabdDir.Close()

	fd, err := unix.Openat(
		int(nabdDir.Fd()),
		"edit.lock",
		unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC,
		0o600,
	)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("refusing symlinked edit.lock: %w", err)
		}
		return nil, fmt.Errorf("open edit.lock: %w", err)
	}
	f := os.NewFile(uintptr(fd), ".nabd/edit.lock")
	if f == nil {
		_ = unix.Close(fd)
		return nil, errors.New("invalid descriptor for edit.lock")
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("refusing non-regular edit.lock file")
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("workspace is locked by another process: %w", err)
	}
	return &processLock{file: f}, nil
}

func (l *processLock) close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}
