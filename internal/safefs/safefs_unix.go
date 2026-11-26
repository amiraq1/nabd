//go:build unix

package safefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// OpenDirAtNoFollow opens directory name relative to parentFD without following
// symlinks. Linux returns ENOTDIR for a symlink when O_DIRECTORY|O_NOFOLLOW is
// specified, so Fstatat(AT_SYMLINK_NOFOLLOW) is used to classify symlinks
// accurately.
func OpenDirAtNoFollow(parentFD int, name string) (*os.File, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%w: %s", ErrSymlink, name)
		}
		if errors.Is(err, unix.ENOTDIR) {
			var st unix.Stat_t
			if fErr := unix.Fstatat(parentFD, name, &st, unix.AT_SYMLINK_NOFOLLOW); fErr == nil && (uint32(st.Mode)&unix.S_IFMT) == unix.S_IFLNK {
				return nil, fmt.Errorf("%w: %s", ErrSymlink, name)
			}
			return nil, fmt.Errorf("%w: %s", ErrNotDirectory, name)
		}
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	if f == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("invalid directory descriptor for %s", name)
	}
	return f, nil
}

// OpenRegularAtNoFollow opens non-directory file name relative to parentFD with
// O_NOFOLLOW|O_NONBLOCK and verifies on the opened descriptor that it is regular.
func OpenRegularAtNoFollow(parentFD int, name string) (*os.File, os.FileInfo, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, nil, fmt.Errorf("%w: %s", ErrSymlink, name)
		}
		if errors.Is(err, unix.EISDIR) {
			return nil, nil, fmt.Errorf("%w: %s", ErrNotRegularFile, name)
		}
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	if f == nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("invalid file descriptor for %s", name)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%w: %s", ErrNotRegularFile, name)
	}
	return f, info, nil
}

// OpenFileNoFollow opens path component-by-component with O_NOFOLLOW from "/",
// rejecting symbolic links anywhere along the path and enforcing pol via fstat.
func OpenFileNoFollow(path string, pol OpenPolicy) (*os.File, os.FileInfo, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsAny(path, "\x00\r\n") {
		return nil, nil, fmt.Errorf("%w: %q", ErrInvalidPath, path)
	}
	if strings.HasSuffix(path, string(filepath.Separator)) {
		return nil, nil, fmt.Errorf("%w: path ends with separator", ErrNotRegularFile)
	}

	var parts []string
	if !filepath.IsAbs(path) {
		wd, err := os.Getwd()
		if err != nil {
			return nil, nil, err
		}
		for _, seg := range strings.Split(wd, string(filepath.Separator)) {
			if seg != "" && seg != "." {
				parts = append(parts, seg)
			}
		}
	}
	for _, seg := range strings.Split(path, string(filepath.Separator)) {
		if seg == "" || seg == "." {
			continue
		}
		parts = append(parts, seg)
	}
	if len(parts) == 0 {
		return nil, nil, fmt.Errorf("%w: %s", ErrNotRegularFile, path)
	}

	curDir, err := openRootLookupDir()
	if err != nil {
		return nil, nil, err
	}

	for i := 0; i < len(parts)-1; i++ {
		nextDir, openErr := openLookupDirAtNoFollow(int(curDir.Fd()), parts[i])
		_ = curDir.Close()
		if openErr != nil {
			return nil, nil, openErr
		}
		curDir = nextDir
	}

	leaf := parts[len(parts)-1]
	if leaf == ".." {
		_ = curDir.Close()
		return nil, nil, fmt.Errorf("%w: %s", ErrNotRegularFile, path)
	}
	f, info, err := OpenRegularAtNoFollow(int(curDir.Fd()), leaf)
	_ = curDir.Close()
	if err != nil {
		return nil, nil, err
	}

	if err := checkUnixPolicy(f, info, pol); err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, info, nil
}

func checkUnixPolicy(f *os.File, info os.FileInfo, pol OpenPolicy) error {
	if pol.MaxBytes > 0 && info.Size() > pol.MaxBytes {
		return fmt.Errorf("%w (size %d > %d bytes)", ErrTooLarge, info.Size(), pol.MaxBytes)
	}
	if pol.RequireOwner {
		var st unix.Stat_t
		if err := unix.Fstat(int(f.Fd()), &st); err != nil {
			return err
		}
		euid := uint32(os.Geteuid())
		if st.Uid != euid {
			return fmt.Errorf("%w (uid %d != euid %d)", ErrOwner, st.Uid, euid)
		}
	}
	perm := info.Mode().Perm()
	if pol.StrictPrivateMode && perm&0o077 != 0 {
		return fmt.Errorf("%w (mode %04o must be 0600 or stricter)", ErrPermission, perm)
	}
	if pol.RejectGroupWrite && perm&0o022 != 0 {
		return fmt.Errorf("%w (mode %04o allows group/other write)", ErrPermission, perm)
	}
	if pol.RejectExecutable && perm&0o111 != 0 {
		return fmt.Errorf("%w (mode %04o must not be executable)", ErrPermission, perm)
	}
	if pol.RequireExecutable && perm&0o111 == 0 {
		return fmt.Errorf("%w (mode %04o is not executable)", ErrNotExecutable, perm)
	}
	return nil
}
