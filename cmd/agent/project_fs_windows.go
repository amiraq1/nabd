//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/sys/windows"
)

// Windows Win32 has no openat(dirfd) equivalent in Go stdlib/x/sys; we open
// final files and directory components with FILE_FLAG_OPEN_REPARSE_POINT and
// check handle attributes, which prevents following symlinks/junctions on the
// checked handles, though a local attacker could still race parent directory
// replacement between directory verification and child open.
const projectSymlinkSupportNote = "windows FILE_FLAG_OPEN_REPARSE_POINT (intermediate dir swap window remains on Win32)"

func openFileNoReparseWindows(fullPath string) (*os.File, windows.ByHandleFileInformation, error) {
	var bhfi windows.ByHandleFileInformation
	utf16Path, err := windows.UTF16PtrFromString(fullPath)
	if err != nil {
		return nil, bhfi, err
	}
	h, err := windows.CreateFile(
		utf16Path,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, bhfi, mapWindowsOpenErr(err)
	}
	f := os.NewFile(uintptr(h), fullPath)
	if f == nil {
		_ = windows.CloseHandle(h)
		return nil, bhfi, fmt.Errorf("invalid file handle for %s", fullPath)
	}
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &bhfi); err != nil {
		_ = f.Close()
		return nil, bhfi, err
	}
	if bhfi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = f.Close()
		return nil, bhfi, errProjectSymlink
	}
	if bhfi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = f.Close()
		return nil, bhfi, errProjectNotRegularFile
	}
	return f, bhfi, nil
}

func verifyDirNoReparseWindows(fullPath string) (*os.File, error) {
	utf16Path, err := windows.UTF16PtrFromString(fullPath)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(
		utf16Path,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, mapWindowsOpenErr(err)
	}
	f := os.NewFile(uintptr(h), fullPath)
	if f == nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("invalid directory handle for %s", fullPath)
	}
	var bhfi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &bhfi); err != nil {
		_ = f.Close()
		return nil, err
	}
	if bhfi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = f.Close()
		return nil, errProjectSymlink
	}
	if bhfi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		_ = f.Close()
		return nil, errProjectNotDirectory
	}
	return f, nil
}

func mapWindowsOpenErr(err error) error {
	if err == windows.ERROR_FILE_NOT_FOUND || err == windows.ERROR_PATH_NOT_FOUND {
		return os.ErrNotExist
	}
	return err
}

func readBoundedWindowsFile(f *os.File, maxBytes int64) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errProjectNotRegularFile
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%w (size %d > %d bytes)", errProjectFileTooLarge, info.Size(), maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w (exceeds %d bytes)", errProjectFileTooLarge, maxBytes)
	}
	return data, nil
}

func readRootFileNoFollow(root *os.Root, name string, maxBytes int64) ([]byte, error) {
	fullPath := filepath.Join(root.Name(), name)
	f, _, err := openFileNoReparseWindows(fullPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readBoundedWindowsFile(f, maxBytes)
}

func scanCommandsDirNoFollow(
	ctx context.Context,
	root *os.Root,
	maxEntries int,
	visit func(name string, isDir, isSymlink bool, readFn func(maxBytes int64) ([]byte, error)) bool,
) (bool, error) {
	nabdPath := filepath.Join(root.Name(), ".nabd")
	hNabd, err := verifyDirNoReparseWindows(nabdPath)
	if err != nil {
		return false, err
	}
	defer hNabd.Close()

	cmdDirPath := filepath.Join(nabdPath, "commands")
	hCmds, err := verifyDirNoReparseWindows(cmdDirPath)
	if err != nil {
		return false, err
	}
	defer hCmds.Close()

	dirFile, err := os.Open(cmdDirPath)
	if err != nil {
		return false, err
	}
	defer dirFile.Close()

	var rawEntries []os.DirEntry
	incomplete := false
	for len(rawEntries) <= maxEntries {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		batch, readErr := dirFile.ReadDir(min(64, maxEntries+1-len(rawEntries)))
		rawEntries = append(rawEntries, batch...)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return true, readErr
		}
		if len(batch) == 0 {
			break
		}
	}
	if len(rawEntries) > maxEntries {
		incomplete = true
		rawEntries = rawEntries[:maxEntries]
	}

	sort.Slice(rawEntries, func(i, j int) bool {
		return rawEntries[i].Name() < rawEntries[j].Name()
	})

	for _, de := range rawEntries {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		name := de.Name()
		isDir := de.IsDir()
		isSymlink := de.Type()&os.ModeSymlink != 0
		readFn := func(maxBytes int64) ([]byte, error) {
			f, _, oErr := openFileNoReparseWindows(filepath.Join(cmdDirPath, name))
			if oErr != nil {
				return nil, oErr
			}
			defer f.Close()
			return readBoundedWindowsFile(f, maxBytes)
		}
		if !visit(name, isDir, isSymlink, readFn) {
			break
		}
	}
	return incomplete, nil
}
