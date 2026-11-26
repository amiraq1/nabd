//go:build windows

package tools

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// openSafeWebKeyFile opens path with FILE_FLAG_OPEN_REPARSE_POINT and validates
// attributes, size, link count, and broad-group DACL entries on the opened
// handle (no separate Lstat-then-Open race). POSIX 0600 mode bits are ignored
// on Windows; confidentiality depends on NTFS ACLs of the key file and parent
// user profile directory.
func openSafeWebKeyFile(path string) (*os.File, error) {
	utf16Path, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("invalid key file path %s: %w", path, err)
	}
	h, err := windows.CreateFile(
		utf16Path,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open key file %s: %w", path, err)
	}
	f := os.NewFile(uintptr(h), path)
	if f == nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("invalid key file handle for %s", path)
	}

	var bhfi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &bhfi); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("inspect key file handle %s: %w", path, err)
	}
	if bhfi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("insecure key file: symlinks/reparse points are not allowed: %s", path)
	}
	if bhfi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("insecure key file: not a regular file: %s", path)
	}
	if bhfi.NumberOfLinks > 1 {
		_ = f.Close()
		return nil, fmt.Errorf("refusing multiply-linked key file %s (link count %d)", path, bhfi.NumberOfLinks)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat key file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("insecure key file: not a regular file: %s", path)
	}
	if info.Size() > maxWebKeyFileBytes {
		_ = f.Close()
		return nil, fmt.Errorf("insecure key file: size %d bytes exceeds %d-byte limit: %s", info.Size(), maxWebKeyFileBytes, path)
	}

	if err := checkWindowsKeyFileDACL(windows.Handle(f.Fd()), path); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func checkWindowsKeyFileDACL(h windows.Handle, path string) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("query key file ACL for %s: %w", path, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("insecure key file ACL for %s: %w", path, err)
	}
	if dacl == nil {
		return fmt.Errorf("insecure key file ACL for %s: empty DACL allows unrestricted access", path)
	}

	worldSID, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		return err
	}
	usersSID, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		return err
	}
	anonSID, err := windows.CreateWellKnownSid(windows.WinAnonymousSid)
	if err != nil {
		return err
	}

	const readMask = windows.FILE_READ_DATA | windows.FILE_GENERIC_READ | windows.GENERIC_READ | windows.GENERIC_ALL | windows.MAXIMUM_ALLOWED
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("inspect key file ACE for %s: %w", path, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if (sid.Equals(worldSID) || sid.Equals(usersSID) || sid.Equals(anonSID)) && (ace.Mask&readMask != 0) {
			return fmt.Errorf("insecure key file ACL for %s: broad group (Everyone/Users/Anonymous) has read access", path)
		}
	}
	return nil
}
