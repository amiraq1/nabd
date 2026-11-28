//go:build windows

package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/amiraq1/nabd/internal/safefs"
	"golang.org/x/sys/windows"
)

type memoryDirHandle struct {
	nabdDir *os.File
	memDir  *os.File
	memPath string
}

func (h *memoryDirHandle) close() {
	if h == nil {
		return
	}
	if h.memDir != nil {
		_ = h.memDir.Close()
		h.memDir = nil
	}
	if h.nabdDir != nil {
		_ = h.nabdDir.Close()
		h.nabdDir = nil
	}
}

type memoryLock struct {
	file *os.File
}

func (l *memoryLock) close() error {
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

// directorySyncSupported reports false on Windows because Win32 does not expose
// portable directory fsync.
func directorySyncSupported() bool { return false }

func syncDirectoryFD(*os.File) error { return nil }

func openMemoryDir(root *os.Root, create bool) (*memoryDirHandle, error) {
	if root == nil {
		return nil, errors.New("memory: nil root")
	}
	nabdPath := filepath.Join(root.Name(), dirParent)
	if create {
		if err := root.Mkdir(dirParent, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("memory: create %s: %w", dirParent, err)
		}
	}
	nabdDir, err := safefs.VerifyDirNoReparseWindows(nabdPath)
	if err != nil {
		return nil, err
	}

	memPath := filepath.Join(nabdPath, dirName)
	if create {
		if err := os.Mkdir(memPath, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			_ = nabdDir.Close()
			return nil, fmt.Errorf("memory: create %s: %w", dirName, err)
		}
	}
	memDir, err := safefs.VerifyDirNoReparseWindows(memPath)
	if err != nil {
		_ = nabdDir.Close()
		return nil, err
	}
	return &memoryDirHandle{
		nabdDir: nabdDir,
		memDir:  memDir,
		memPath: memPath,
	}, nil
}

func acquireMemoryLock(ctx context.Context, h *memoryDirHandle, wait time.Duration) (*memoryLock, error) {
	if h == nil || h.memPath == "" {
		return nil, errors.New("memory: nil directory handle")
	}
	lockPath := filepath.Join(h.memPath, lockFileName)
	utf16Path, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		utf16Path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("memory: open %s: %w", lockFileName, err)
	}
	f := os.NewFile(uintptr(handle), dirParent+"/"+dirName+"/"+lockFileName)
	if f == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("memory: invalid handle for %s", lockFileName)
	}
	var bhfi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &bhfi); err != nil {
		_ = f.Close()
		return nil, err
	}
	if bhfi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("memory: refusing reparse point %s", lockFileName)
	}
	if bhfi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("memory: refusing directory as %s", lockFileName)
	}
	if bhfi.NumberOfLinks > 1 {
		_ = f.Close()
		return nil, fmt.Errorf("memory: refusing multiply-linked %s (nlink=%d)", lockFileName, bhfi.NumberOfLinks)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("memory: refusing non-regular %s", lockFileName)
	}

	deadline := time.Now().Add(wait)
	ticker := time.NewTicker(lockRetryInterval)
	defer ticker.Stop()

	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		var overlapped windows.Overlapped
		err = windows.LockFileEx(
			windows.Handle(f.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, 1, 0, &overlapped,
		)
		if err == nil {
			return &memoryLock{file: f}, nil
		}
		if err != windows.ERROR_LOCK_VIOLATION {
			_ = f.Close()
			return nil, fmt.Errorf("memory: lock %s: %w", lockFileName, err)
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("memory: timed out after %s waiting for %s: %w", wait, lockFileName, ErrLockBusy)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func readEntriesFile(h *memoryDirHandle, maxBytes int64) ([]byte, error) {
	if h == nil || h.memPath == "" {
		return nil, errors.New("memory: nil directory handle")
	}
	fullPath := filepath.Join(h.memPath, entriesFileName)
	f, info, err := safefs.OpenRegularNoReparseWindows(fullPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var bhfi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &bhfi); err != nil {
		return nil, fmt.Errorf("memory: inspect %s: %w", entriesFileName, err)
	}
	if bhfi.NumberOfLinks > 1 {
		return nil, fmt.Errorf("%w: %s has link count %d", ErrCorruptStore, entriesFileName, bhfi.NumberOfLinks)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes (%d bytes)", ErrLimitExceeded, entriesFileName, maxBytes, info.Size())
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("memory: read %s: %w", entriesFileName, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrLimitExceeded, entriesFileName, maxBytes)
	}
	return data, nil
}

func writeEntriesAtomic(
	h *memoryDirHandle,
	data []byte,
	syncDir func(*os.File) error,
	beforeRename func(),
	writeHook func(*os.File, []byte) error,
) error {
	if h == nil || h.memPath == "" {
		return errors.New("memory: nil directory handle")
	}
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Errorf("memory: temp file entropy: %w", err)
	}
	tmpName := ".tmp-" + hex.EncodeToString(rnd[:])
	tmpPath := filepath.Join(h.memPath, tmpName)
	utf16Tmp, err := windows.UTF16PtrFromString(tmpPath)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(
		utf16Tmp,
		windows.GENERIC_WRITE,
		0,
		nil,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return fmt.Errorf("memory: create temp file: %w", err)
	}
	f := os.NewFile(uintptr(handle), tmpName)
	if f == nil {
		_ = windows.CloseHandle(handle)
		_ = os.Remove(tmpPath)
		return errors.New("memory: invalid temp handle")
	}

	var writeErr error
	if writeHook != nil {
		writeErr = writeHook(f, data)
	} else {
		_, writeErr = f.Write(data)
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("memory: write temp file: %w", err)
	}

	if beforeRename != nil {
		beforeRename()
	}
	targetPath := filepath.Join(h.memPath, entriesFileName)
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("memory: replace %s: %w", entriesFileName, err)
	}
	if syncDir != nil {
		if err := syncDir(h.memDir); err != nil {
			return &CommitUncertainError{Err: err}
		}
	}
	return nil
}
