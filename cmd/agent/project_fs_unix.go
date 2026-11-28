//go:build unix

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/amiraq1/nabd/internal/safefs"
)

const (
	projectSymlinkSupportNote      = "unix openat(O_NOFOLLOW|O_NONBLOCK)"
	projectSkillsPlatformSupported = true
)

func mapProjectFSErr(err error, part string) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, safefs.ErrSymlink):
		if part != "" {
			return fmt.Errorf("%w: %s", errProjectSymlink, part)
		}
		return errProjectSymlink
	case errors.Is(err, safefs.ErrNotDirectory):
		if part != "" {
			return fmt.Errorf("%w: %s", errProjectNotDirectory, part)
		}
		return errProjectNotDirectory
	case errors.Is(err, safefs.ErrNotRegularFile):
		return errProjectNotRegularFile
	case errors.Is(err, safefs.ErrTooLarge):
		return fmt.Errorf("%w (%v)", errProjectFileTooLarge, err)
	default:
		return err
	}
}

func openDirComponentNoFollow(parentFD int, name string) (*os.File, error) {
	return safefs.OpenDirAtNoFollow(parentFD, name)
}

func openRegularFileNoFollow(parentFD int, name string) (*os.File, os.FileInfo, error) {
	return safefs.OpenRegularAtNoFollow(parentFD, name)
}

func readBoundedRegularNoFollow(f *os.File, info os.FileInfo, maxBytes int64) ([]byte, error) {
	data, err := safefs.ReadBoundedRegular(f, info, maxBytes)
	if err != nil {
		return nil, mapProjectFSErr(err, "")
	}
	return data, nil
}

func readRootFileNoFollow(root *os.Root, name string, maxBytes int64) ([]byte, error) {
	rootDir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer rootDir.Close()

	f, info, err := openRegularFileNoFollow(int(rootDir.Fd()), name)
	if err != nil {
		return nil, mapProjectFSErr(err, "")
	}
	defer f.Close()
	return readBoundedRegularNoFollow(f, info, maxBytes)
}

func scanCommandsDirNoFollow(
	ctx context.Context,
	root *os.Root,
	maxEntries int,
	visit func(name string, isDir, isSymlink bool, readFn func(maxBytes int64) ([]byte, error)) bool,
) (bool, error) {
	curDir, err := root.Open(".")
	if err != nil {
		return false, err
	}

	for _, part := range []string{".nabd", "commands"} {
		if err := ctx.Err(); err != nil {
			_ = curDir.Close()
			return false, err
		}
		nextDir, openErr := openDirComponentNoFollow(int(curDir.Fd()), part)
		if openErr != nil {
			_ = curDir.Close()
			return false, mapProjectFSErr(openErr, part)
		}
		_ = curDir.Close()
		curDir = nextDir
	}
	defer curDir.Close()

	var rawEntries []os.DirEntry
	incomplete := false
	for len(rawEntries) <= maxEntries {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		batch, readErr := curDir.ReadDir(min(64, maxEntries+1-len(rawEntries)))
		rawEntries = append(rawEntries, batch...)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return true, readErr
		}
		if len(batch) == 0 {
			break
		}
	}
	if len(rawEntries) > maxEntries {
		incomplete = true
		rawEntries = rawEntries[:maxEntries]
	}

	sort.Slice(rawEntries, func(i, j int) bool {
		return rawEntries[i].Name() < rawEntries[j].Name()
	})

	dirFD := int(curDir.Fd())
	for _, de := range rawEntries {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		name := de.Name()
		isDir := de.IsDir()
		isSymlink := de.Type()&os.ModeSymlink != 0
		readFn := func(maxBytes int64) ([]byte, error) {
			f, info, oErr := openRegularFileNoFollow(dirFD, name)
			if oErr != nil {
				return nil, mapProjectFSErr(oErr, "")
			}
			defer f.Close()
			return readBoundedRegularNoFollow(f, info, maxBytes)
		}
		if !visit(name, isDir, isSymlink, readFn) {
			break
		}
	}

	return incomplete, nil
}

func scanSkillsDirNoFollow(
	ctx context.Context,
	root *os.Root,
	maxEntries int,
	visit func(name string, readHeaderFn func(maxBytes int64) ([]byte, bool, error)) bool,
) (bool, error) {
	curDir, err := root.Open(".")
	if err != nil {
		return false, err
	}

	for _, part := range []string{".agents", "skills"} {
		if err := ctx.Err(); err != nil {
			_ = curDir.Close()
			return false, err
		}
		nextDir, openErr := openDirComponentNoFollow(int(curDir.Fd()), part)
		_ = curDir.Close()
		if openErr != nil {
			return false, mapProjectFSErr(openErr, part)
		}
		curDir = nextDir
	}
	defer curDir.Close()

	var rawEntries []os.DirEntry
	incomplete := false
	for len(rawEntries) <= maxEntries {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		batch, readErr := curDir.ReadDir(min(64, maxEntries+1-len(rawEntries)))
		rawEntries = append(rawEntries, batch...)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return true, readErr
		}
		if len(batch) == 0 {
			break
		}
	}
	if len(rawEntries) > maxEntries {
		incomplete = true
		rawEntries = rawEntries[:maxEntries]
	}

	sort.Slice(rawEntries, func(i, j int) bool {
		return rawEntries[i].Name() < rawEntries[j].Name()
	})

	dirFD := int(curDir.Fd())
	for _, de := range rawEntries {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		name := de.Name()
		readHeaderFn := func(maxBytes int64) ([]byte, bool, error) {
			skillDir, dErr := openDirComponentNoFollow(dirFD, name)
			if dErr != nil {
				return nil, false, mapProjectFSErr(dErr, name)
			}
			defer skillDir.Close()

			f, info, oErr := openRegularFileNoFollow(int(skillDir.Fd()), "SKILL.md")
			if oErr != nil {
				return nil, false, mapProjectFSErr(oErr, "")
			}
			defer f.Close()
			return readBoundedHeader(f, info, maxBytes)
		}
		if !visit(name, readHeaderFn) {
			break
		}
	}

	return incomplete, nil
}

func readSkillFileNoFollow(root *os.Root, skillName string, maxBytes int64) ([]byte, error) {
	if !filepath.IsLocal(skillName) {
		return nil, fmt.Errorf("invalid skill name: %q", skillName)
	}
	curDir, err := root.Open(".")
	if err != nil {
		return nil, err
	}

	for _, part := range []string{".agents", "skills", skillName} {
		nextDir, openErr := openDirComponentNoFollow(int(curDir.Fd()), part)
		_ = curDir.Close()
		if openErr != nil {
			return nil, mapProjectFSErr(openErr, part)
		}
		curDir = nextDir
	}
	defer curDir.Close()

	f, info, err := openRegularFileNoFollow(int(curDir.Fd()), "SKILL.md")
	if err != nil {
		return nil, mapProjectFSErr(err, "")
	}
	defer f.Close()
	return readBoundedRegularNoFollow(f, info, maxBytes)
}
