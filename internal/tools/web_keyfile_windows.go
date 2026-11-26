//go:build windows

package tools

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// validateWebKeyFilePlatform verifies hard-link count and NTFS DACL restrictions
// on the already-opened key file handle. POSIX 0600 mode bits are ignored on
// Windows; confidentiality depends on NTFS ACLs of the key file and parent
// user profile directory.
func validateWebKeyFilePlatform(f *os.File, path string) error {
	if err := ensureNoHardLinks(f); err != nil {
		return err
	}
	return checkWindowsKeyFileDACL(windows.Handle(f.Fd()), path)
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
