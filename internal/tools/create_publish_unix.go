//go:build unix && !linux

package tools

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryRenameNoReplace(_ *os.File, _, _ string) error {
	return errors.ErrUnsupported
}

func isUnsupportedExclusiveRename(err error) bool {
	return errors.Is(err, errors.ErrUnsupported)
}

func isUnsupportedHardLink(err error) bool {
	return errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.EPERM) ||
		errors.Is(err, unix.EXDEV) ||
		errors.Is(err, errors.ErrUnsupported)
}

func isExistErr(err error) bool {
	return errors.Is(err, os.ErrExist) || errors.Is(err, unix.EEXIST)
}
