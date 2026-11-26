//go:build windows

package tools

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

func tryRenameNoReplace(_ *os.File, _, _ string) error {
	return errors.ErrUnsupported
}

func isUnsupportedExclusiveRename(err error) bool {
	return errors.Is(err, errors.ErrUnsupported)
}

func isUnsupportedHardLink(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SUPPORTED) ||
		errors.Is(err, windows.ERROR_INVALID_FUNCTION) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EPERM) ||
		errors.Is(err, errors.ErrUnsupported)
}

func isExistErr(err error) bool {
	return errors.Is(err, os.ErrExist) ||
		errors.Is(err, windows.ERROR_ALREADY_EXISTS) ||
		errors.Is(err, windows.ERROR_FILE_EXISTS)
}
