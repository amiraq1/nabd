// Package safefs opens regular files without following symbolic links along
// path components and enforces explicit ownership, mode, and size policies on
// the opened descriptor before reading.
package safefs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

var (
	ErrSymlink        = errors.New("symbolic links are not allowed")
	ErrNotDirectory   = errors.New("not a directory")
	ErrNotRegularFile = errors.New("not a regular file")
	ErrTooLarge       = errors.New("file exceeds size limit")
	ErrInvalidPath    = errors.New("invalid path")
	ErrPermission     = errors.New("insecure file permissions")
	ErrNotExecutable  = errors.New("file is not executable")
	ErrOwner          = errors.New("file owner does not match current user")
)

// OpenPolicy controls descriptor-level security checks performed when opening a
// file via OpenFileNoFollow.
type OpenPolicy struct {
	MaxBytes          int64
	RequireOwner      bool
	RejectGroupWrite  bool // Unix: reject perm & 0o022 != 0
	StrictPrivateMode bool // Unix: reject perm & 0o077 != 0
	RejectExecutable  bool // Unix: reject perm & 0o111 != 0
	RequireExecutable bool // Unix: require perm & 0o111 != 0 (no-op on Windows)
}

// ReadBoundedRegular reads an already-opened regular file up to maxBytes (when
// maxBytes > 0), verifying size before and during reading.
func ReadBoundedRegular(f *os.File, info os.FileInfo, maxBytes int64) ([]byte, error) {
	if !info.Mode().IsRegular() {
		return nil, ErrNotRegularFile
	}
	if maxBytes > 0 {
		if info.Size() > maxBytes {
			return nil, fmt.Errorf("%w (size %d > %d bytes)", ErrTooLarge, info.Size(), maxBytes)
		}
		data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > maxBytes {
			return nil, fmt.Errorf("%w (exceeds %d bytes)", ErrTooLarge, maxBytes)
		}
		return data, nil
	}
	return io.ReadAll(f)
}

// ReadFileNoFollow opens path without following symlinks, validates pol on the
// resulting descriptor, and returns bounded contents and file metadata.
func ReadFileNoFollow(path string, pol OpenPolicy) ([]byte, os.FileInfo, error) {
	f, info, err := OpenFileNoFollow(path, pol)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	data, err := ReadBoundedRegular(f, info, pol.MaxBytes)
	if err != nil {
		return nil, nil, err
	}
	return data, info, nil
}

// DigestFileNoFollow opens path without following symlinks, validates pol on the
// opened descriptor, and streams its SHA-256 hex digest without buffering the
// entire file in memory.
func DigestFileNoFollow(path string, pol OpenPolicy) (string, error) {
	f, info, err := OpenFileNoFollow(path, pol)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	var n int64
	if pol.MaxBytes > 0 {
		n, err = io.Copy(h, io.LimitReader(f, pol.MaxBytes+1))
		if err != nil {
			return "", err
		}
		if n > pol.MaxBytes {
			return "", fmt.Errorf("%w (exceeds %d bytes)", ErrTooLarge, pol.MaxBytes)
		}
	} else {
		n, err = io.Copy(h, f)
		if err != nil {
			return "", err
		}
	}
	if n != info.Size() {
		return "", fmt.Errorf("file changed while hashing: %s", path)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
