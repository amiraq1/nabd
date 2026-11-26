//go:build windows

package safefs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// OpenRegularNoReparseWindows opens fullPath with FILE_FLAG_OPEN_REPARSE_POINT
// and rejects reparse points and directories on the opened handle.
func OpenRegularNoReparseWindows(fullPath string) (*os.File, os.FileInfo, error) {
	utf16Path, err := windows.UTF16PtrFromString(fullPath)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, mapWindowsOpenErr(err)
	}
	f := os.NewFile(uintptr(h), fullPath)
	if f == nil {
		_ = windows.CloseHandle(h)
		return nil, nil, fmt.Errorf("invalid file handle for %s", fullPath)
	}
	var bhfi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &bhfi); err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if bhfi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = f.Close()
		return nil, nil, ErrSymlink
	}
	if bhfi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = f.Close()
		return nil, nil, ErrNotRegularFile
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, ErrNotRegularFile
	}
	return f, info, nil
}

// VerifyDirNoReparseWindows opens fullPath with FILE_FLAG_OPEN_REPARSE_POINT
// and verifies it is a non-reparse directory. Note Win32 lacks openat(dirfd),
// so an intermediate directory swap window remains between check and child open.
func VerifyDirNoReparseWindows(fullPath string) (*os.File, error) {
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
		return nil, ErrSymlink
	}
	if bhfi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		_ = f.Close()
		return nil, ErrNotDirectory
	}
	return f, nil
}

func mapWindowsOpenErr(err error) error {
	if err == windows.ERROR_FILE_NOT_FOUND || err == windows.ERROR_PATH_NOT_FOUND {
		return os.ErrNotExist
	}
	return err
}

// OpenFileNoFollow verifies each directory component along path with
// FILE_FLAG_OPEN_REPARSE_POINT and opens the regular leaf file without
// following reparse points. POSIX permission/UID bits are not enforced on
// Windows; access control depends on NTFS ACLs.
func OpenFileNoFollow(path string, pol OpenPolicy) (*os.File, os.FileInfo, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsAny(path, "\x00\r\n") {
		return nil, nil, fmt.Errorf("%w: %q", ErrInvalidPath, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	vol := filepath.VolumeName(abs)
	rest := strings.TrimPrefix(abs, vol)
	rest = strings.TrimLeft(rest, `\/`)
	if rest == "" {
		return nil, nil, fmt.Errorf("%w: %s", ErrNotRegularFile, path)
	}
	parts := strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' })
	cur := vol + `\`
	var heldDirs []*os.File
	defer func() {
		for _, d := range heldDirs {
			_ = d.Close()
		}
	}()
	for i := 0; i < len(parts)-1; i++ {
		cur = filepath.Join(cur, parts[i])
		d, dErr := VerifyDirNoReparseWindows(cur)
		if dErr != nil {
			return nil, nil, dErr
		}
		heldDirs = append(heldDirs, d)
	}
	fullLeaf := filepath.Join(cur, parts[len(parts)-1])
	f, info, err := OpenRegularNoReparseWindows(fullLeaf)
	if err != nil {
		return nil, nil, err
	}
	if pol.MaxBytes > 0 && info.Size() > pol.MaxBytes {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%w (size %d > %d bytes)", ErrTooLarge, info.Size(), pol.MaxBytes)
	}
	return f, info, nil
}
