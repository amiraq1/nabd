//go:build windows

package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/amiraq1/nabd/internal/safefs"
	"golang.org/x/sys/windows"
)

type processLock struct {
	file *os.File
}

func acquireProcessLock(root *os.Root) (*processLock, error) {
	if root == nil {
		return nil, errors.New("nil workspace root")
	}
	if err := root.Mkdir(".nabd", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create .nabd lock directory: %w", err)
	}
	nabdPath := filepath.Join(root.Name(), ".nabd")
	nabdDir, err := safefs.VerifyDirNoReparseWindows(nabdPath)
	if err != nil {
		return nil, fmt.Errorf("verify .nabd lock directory: %w", err)
	}
	defer nabdDir.Close()

	lockPath := filepath.Join(nabdPath, "edit.lock")
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
		return nil, fmt.Errorf("open edit.lock: %w", err)
	}
	f := os.NewFile(uintptr(h), ".nabd/edit.lock")
	if f == nil {
		_ = windows.CloseHandle(h)
		return nil, errors.New("invalid handle for edit.lock")
	}
	var bhfi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &bhfi); err != nil {
		_ = f.Close()
		return nil, err
	}
	if bhfi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("refusing reparse point edit.lock")
	}
	if bhfi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("refusing directory as edit.lock")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("refusing non-regular edit.lock file")
	}

	var overlapped windows.Overlapped
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &overlapped,
	)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("workspace is locked by another process: %w", err)
	}
	return &processLock{file: f}, nil
}

func (l *processLock) close() error {
	if l == nil || l.file == nil {
		return nil
	}
	var overlapped windows.Overlapped
	err := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &overlapped)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}
