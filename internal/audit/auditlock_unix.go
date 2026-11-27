//go:build unix

package audit

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/amiraq1/nabd/internal/safefs"
	"golang.org/x/sys/unix"
)

var errLockBusy = errors.New("audit lock is held by another process")

type auditLock struct {
	file *os.File
}

// openAuditLockDir opens .nabd without following symlinks. create controls
// whether the directory may be created (Open must not write to a read-only
// workspace; Write may).
func openAuditLockDir(root *os.Root, create bool) (*os.File, error) {
	rootDir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer rootDir.Close()

	d, err := safefs.OpenDirAtNoFollow(int(rootDir.Fd()), dir)
	if err == nil {
		return d, nil
	}
	if !create {
		return nil, err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("audit: open .nabd lock directory: %w", err)
	}
	if mkErr := unix.Mkdirat(int(rootDir.Fd()), dir, 0o700); mkErr != nil && !errors.Is(mkErr, unix.EEXIST) {
		return nil, fmt.Errorf("audit: create .nabd lock directory: %w", mkErr)
	}
	return safefs.OpenDirAtNoFollow(int(rootDir.Fd()), dir)
}

func acquireAuditLock(root *os.Root, wait time.Duration) (*auditLock, error) {
	if root == nil {
		return nil, errors.New("audit: nil root")
	}
	d, err := openAuditLockDir(root, true)
	if err != nil {
		return nil, err
	}
	defer d.Close()

	fd, err := unix.Openat(
		int(d.Fd()),
		lockName,
		unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK,
		0o600,
	)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("audit: refusing symlinked %s: %w", lockName, err)
		}
		return nil, fmt.Errorf("audit: open %s: %w", lockName, err)
	}
	f := os.NewFile(uintptr(fd), dir+"/"+lockName)
	if f == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("audit: invalid descriptor for %s", lockName)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("audit: refusing non-regular %s", lockName)
	}

	deadline := time.Now().Add(wait)
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &auditLock{file: f}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = f.Close()
			return nil, fmt.Errorf("audit: lock %s: %w", lockName, err)
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("audit: timed out after %s waiting for %s: %w", wait, lockName, errLockBusy)
		}
		time.Sleep(lockRetryInterval)
	}
}

func (l *auditLock) close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
