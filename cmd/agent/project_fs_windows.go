//go:build windows

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

// Windows Win32 has no openat(dirfd) equivalent in Go stdlib/x/sys; we open
// final files and directory components with FILE_FLAG_OPEN_REPARSE_POINT and
// check handle attributes, which prevents following symlinks/junctions on the
// checked handles, though a local attacker could still race parent directory
// replacement between directory verification and child open.
const (
	projectSymlinkSupportNote      = "windows FILE_FLAG_OPEN_REPARSE_POINT (intermediate dir swap window remains on Win32)"
	projectSkillsPlatformSupported = true
)

func mapProjectFSWindowsErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, safefs.ErrSymlink):
		return errProjectSymlink
	case errors.Is(err, safefs.ErrNotDirectory):
		return errProjectNotDirectory
	case errors.Is(err, safefs.ErrNotRegularFile):
		return errProjectNotRegularFile
	case errors.Is(err, safefs.ErrTooLarge):
		return fmt.Errorf("%w (%v)", errProjectFileTooLarge, err)
	default:
		return err
	}
}

func readBoundedWindowsFile(f *os.File, info os.FileInfo, maxBytes int64) ([]byte, error) {
	data, err := safefs.ReadBoundedRegular(f, info, maxBytes)
	if err != nil {
		return nil, mapProjectFSWindowsErr(err)
	}
	return data, nil
}

func readRootFileNoFollow(root *os.Root, name string, maxBytes int64) ([]byte, error) {
	fullPath := filepath.Join(root.Name(), name)
	f, info, err := safefs.OpenRegularNoReparseWindows(fullPath)
	if err != nil {
		return nil, mapProjectFSWindowsErr(err)
	}
	defer f.Close()
	return readBoundedWindowsFile(f, info, maxBytes)
}

func scanCommandsDirNoFollow(
	ctx context.Context,
	root *os.Root,
	maxEntries int,
	visit func(name string, isDir, isSymlink bool, readFn func(maxBytes int64) ([]byte, error)) bool,
) (bool, error) {
	nabdPath := filepath.Join(root.Name(), ".nabd")
	hNabd, err := safefs.VerifyDirNoReparseWindows(nabdPath)
	if err != nil {
		return false, mapProjectFSWindowsErr(err)
	}
	defer hNabd.Close()

	cmdDirPath := filepath.Join(nabdPath, "commands")
	hCmds, err := safefs.VerifyDirNoReparseWindows(cmdDirPath)
	if err != nil {
		return false, mapProjectFSWindowsErr(err)
	}
	defer hCmds.Close()

	dirFile, err := os.Open(cmdDirPath)
	if err != nil {
		return false, err
	}
	defer dirFile.Close()

	var rawEntries []os.DirEntry
	incomplete := false
	for len(rawEntries) <= maxEntries {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		batch, readErr := dirFile.ReadDir(min(64, maxEntries+1-len(rawEntries)))
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

	for _, de := range rawEntries {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		name := de.Name()
		isDir := de.IsDir()
		isSymlink := de.Type()&os.ModeSymlink != 0
		readFn := func(maxBytes int64) ([]byte, error) {
			f, info, oErr := safefs.OpenRegularNoReparseWindows(filepath.Join(cmdDirPath, name))
			if oErr != nil {
				return nil, mapProjectFSWindowsErr(oErr)
			}
			defer f.Close()
			return readBoundedWindowsFile(f, info, maxBytes)
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
	agentsPath := filepath.Join(root.Name(), ".agents")
	hAgents, err := safefs.VerifyDirNoReparseWindows(agentsPath)
	if err != nil {
		return false, mapProjectFSWindowsErr(err)
	}
	defer hAgents.Close()

	skillsPath := filepath.Join(agentsPath, "skills")
	hSkills, err := safefs.VerifyDirNoReparseWindows(skillsPath)
	if err != nil {
		return false, mapProjectFSWindowsErr(err)
	}
	defer hSkills.Close()

	dirFile, err := os.Open(skillsPath)
	if err != nil {
		return false, err
	}
	defer dirFile.Close()

	var rawEntries []os.DirEntry
	incomplete := false
	for len(rawEntries) <= maxEntries {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		batch, readErr := dirFile.ReadDir(min(64, maxEntries+1-len(rawEntries)))
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

	for _, de := range rawEntries {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		name := de.Name()
		readHeaderFn := func(maxBytes int64) ([]byte, bool, error) {
			skillDirPath := filepath.Join(skillsPath, name)
			hSkill, dErr := safefs.VerifyDirNoReparseWindows(skillDirPath)
			if dErr != nil {
				return nil, false, mapProjectFSWindowsErr(dErr)
			}
			defer hSkill.Close()

			f, info, oErr := safefs.OpenRegularNoReparseWindows(filepath.Join(skillDirPath, "SKILL.md"))
			if oErr != nil {
				return nil, false, mapProjectFSWindowsErr(oErr)
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
	agentsPath := filepath.Join(root.Name(), ".agents")
	hAgents, err := safefs.VerifyDirNoReparseWindows(agentsPath)
	if err != nil {
		return nil, mapProjectFSWindowsErr(err)
	}
	defer hAgents.Close()

	skillsPath := filepath.Join(agentsPath, "skills")
	hSkills, err := safefs.VerifyDirNoReparseWindows(skillsPath)
	if err != nil {
		return nil, mapProjectFSWindowsErr(err)
	}
	defer hSkills.Close()

	skillDirPath := filepath.Join(skillsPath, skillName)
	hSkill, err := safefs.VerifyDirNoReparseWindows(skillDirPath)
	if err != nil {
		return nil, mapProjectFSWindowsErr(err)
	}
	defer hSkill.Close()

	f, info, err := safefs.OpenRegularNoReparseWindows(filepath.Join(skillDirPath, "SKILL.md"))
	if err != nil {
		return nil, mapProjectFSWindowsErr(err)
	}
	defer f.Close()
	return readBoundedWindowsFile(f, info, maxBytes)
}
