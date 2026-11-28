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

// absolutePathCleanKept returns p as an absolute path with "." and ".."
// components preserved. filepath.Abs cannot be used because it runs Clean,
// which removes ".." together with the component in front of it and would let
// a reparse point on that component go uninspected.
func absolutePathCleanKept(p string) (string, error) {
	if filepath.IsAbs(p) {
		return p, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return wd + `\` + p, nil
}

// OpenFileNoFollow verifies each directory component along path with
// FILE_FLAG_OPEN_REPARSE_POINT and opens the regular leaf file without
// following reparse points. POSIX permission/UID bits are not enforced on
// Windows; access control depends on NTFS ACLs.
//
// Components are classified from the path as written. filepath.Abs applies
// Clean, which removes ".." textually and drops the component before it: for
// "symdir/../real/k" the "symdir" reparse point would never be inspected and
// the protection silently disappears. POSIX opens every component with
// O_NOFOLLOW and fails with ELOOP there; walking the raw components gives the
// same answer on Windows.
func OpenFileNoFollow(path string, pol OpenPolicy) (*os.File, os.FileInfo, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsAny(path, "\x00\r\n") {
		return nil, nil, fmt.Errorf("%w: %q", ErrInvalidPath, path)
	}
	abs, err := absolutePathCleanKept(path)
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
	last := -1
	for i, part := range parts {
		if part != "." && part != ".." {
			last = i
		}
	}
	if last < 0 {
		return nil, nil, fmt.Errorf("%w: %s", ErrNotRegularFile, path)
	}
	var heldDirs []*os.File
	defer func() {
		for _, d := range heldDirs {
			_ = d.Close()
		}
	}()
	for i, part := range parts {
		switch part {
		case ".":
			continue
		case "..":
			// The parent was already verified as a real directory and is held
			// open; resolving ".." against it stays inside the verified chain.
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, part)
		if i == last {
			cur = next
			continue
		}
		d, dErr := VerifyDirNoReparseWindows(next)
		if dErr != nil {
			return nil, nil, dErr
		}
		heldDirs = append(heldDirs, d)
		cur = next
	}
	f, info, err := OpenRegularNoReparseWindows(cur)
	if err != nil {
		return nil, nil, err
	}
	if pol.MaxBytes > 0 && info.Size() > pol.MaxBytes {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%w (size %d > %d bytes)", ErrTooLarge, info.Size(), pol.MaxBytes)
	}
	return f, info, nil
}
