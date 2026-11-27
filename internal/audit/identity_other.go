//go:build !unix && !windows

package audit

import (
	"errors"
	"os"
)

type fileID struct{}

func fileIdentity(*os.File) (fileID, error) {
	return fileID{}, errors.New("audit: file identity is unsupported on this platform")
}
