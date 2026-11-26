//go:build !unix && !windows

package tools

import (
	"errors"
	"os"
)

func tryRenameNoReplace(_ *os.File, _, _ string) error {
	return errors.ErrUnsupported
}

func isUnsupportedExclusiveRename(err error) bool {
	return errors.Is(err, errors.ErrUnsupported)
}

func isUnsupportedHardLink(err error) bool {
	return errors.Is(err, errors.ErrUnsupported)
}

func isExistErr(err error) bool {
	return errors.Is(err, os.ErrExist)
}
