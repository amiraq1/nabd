//go:build unix

package tools

import (
	"fmt"
	"os"

	"github.com/amiraq1/nabd/internal/safefs"
)

func verifyOpenedDirNoFollow(parentRoot *os.Root, part string, childRoot *os.Root) error {
	parentDir, err := parentRoot.Open(".")
	if err != nil {
		return err
	}
	defer parentDir.Close()

	nfDir, err := safefs.OpenDirAtNoFollow(int(parentDir.Fd()), part)
	if err != nil {
		return err
	}
	defer nfDir.Close()

	nfInfo, err := nfDir.Stat()
	if err != nil {
		return err
	}
	childDir, err := childRoot.Open(".")
	if err != nil {
		return err
	}
	defer childDir.Close()

	childInfo, err := childDir.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(nfInfo, childInfo) {
		return fmt.Errorf("symlink or concurrent directory replacement detected at %s", part)
	}
	return nil
}
