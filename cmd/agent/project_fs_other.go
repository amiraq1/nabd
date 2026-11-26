//go:build !unix && !windows

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const projectSymlinkSupportNote = "fallback os.Root Lstat+Open (non-atomic on non-Unix/non-Windows platforms)"

func readRootFileNoFollow(root *os.Root, name string, maxBytes int64) ([]byte, error) {
	linfo, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if linfo.Mode()&os.ModeSymlink != 0 {
		return nil, errProjectSymlink
	}
	if !linfo.Mode().IsRegular() {
		return nil, errProjectNotRegularFile
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errProjectNotRegularFile
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%w (size %d > %d bytes)", errProjectFileTooLarge, info.Size(), maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w (exceeds %d bytes)", errProjectFileTooLarge, maxBytes)
	}
	return data, nil
}

func scanCommandsDirNoFollow(
	ctx context.Context,
	root *os.Root,
	maxEntries int,
	visit func(name string, isDir, isSymlink bool, readFn func(maxBytes int64) ([]byte, error)) bool,
) (bool, error) {
	for _, dirPath := range []string{".nabd", filepath.Join(".nabd", "commands")} {
		info, err := root.Lstat(dirPath)
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return false, errProjectSymlink
		}
		if !info.IsDir() {
			return false, errProjectNotDirectory
		}
	}

	cmdDirRel := filepath.Join(".nabd", "commands")
	dirFile, err := root.Open(cmdDirRel)
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
			return readRootFileNoFollow(root, filepath.Join(cmdDirRel, name), maxBytes)
		}
		if !visit(name, isDir, isSymlink, readFn) {
			break
		}
	}
	return incomplete, nil
}
