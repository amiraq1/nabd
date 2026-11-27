//go:build windows

package audit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/amiraq1/nabd/internal/safefs"
	"golang.org/x/sys/windows"
)

var errLockBusy = errors.New("audit lock is held by another process")

type auditLock struct {
	file *os.File
}

func acquireAuditLock(root *os.Root, wait time.Duration) (*auditLock, error) {
	if root == nil {
		return nil, errors.New("audit: nil root")
	}
	if err := root.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("audit: create .nabd lock directory: %w", err)
	}
	dirPath := filepath.Join(root.Name(), dir)
	dirHandle, err := safefs.VerifyDirNoReparseWindows(dirPath)
	if err != nil {
		return nil, fmt.Errorf("audit: verify .nabd lock directory: %w", err)
	}
	defer dirHandle.Close()

	lockPath := filepath.Join(dirPath, lockName)
	utf16Path, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(
		utf16Path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("audit: open %s: %w", lockName, err)
	}
	f := os.NewFile(uintptr(h), dir+"/"+lockName)
	if f == nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("audit: invalid handle for %s", lockName)
	}
	var bhfi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &bhfi); err != nil {
		_ = f.Close()
		return nil, err
	}
	if bhfi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("audit: refusing reparse point %s", lockName)
	}
	if bhfi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("audit: refusing directory as %s", lockName)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("audit: refusing non-regular %s", lockName)
	}

	deadline := time.Now().Add(wait)
	for {
		var overlapped windows.Overlapped
		err = windows.LockFileEx(
			windows.Handle(f.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, 1, 0, &overlapped,
		)
		if err == nil {
			return &auditLock{file: f}, nil
		}
		if err != windows.ERROR_LOCK_VIOLATION {
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
	var overlapped windows.Overlapped
	unlockErr := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &overlapped)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
