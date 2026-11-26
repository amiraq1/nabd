//go:build unix

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"golang.org/x/sys/unix"
)

const projectSymlinkSupportNote = "unix openat(O_NOFOLLOW|O_NONBLOCK)"

func openDirComponentNoFollow(parentFD int, name string) (*os.File, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	if f == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("invalid directory descriptor for %s", name)
	}
	return f, nil
}

func openRegularFileNoFollow(parentFD int, name string) (*os.File, os.FileInfo, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
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
		return nil, nil, errProjectNotRegularFile
	}
	return f, info, nil
}

func readBoundedRegularNoFollow(f *os.File, info os.FileInfo, maxBytes int64) ([]byte, error) {
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

func readRootFileNoFollow(root *os.Root, name string, maxBytes int64) ([]byte, error) {
	rootDir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer rootDir.Close()

	f, info, err := openRegularFileNoFollow(int(rootDir.Fd()), name)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, errProjectSymlink
		}
		if errors.Is(err, unix.EISDIR) {
			return nil, errProjectNotRegularFile
		}
		return nil, err
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
		_ = curDir.Close()
		if openErr != nil {
			if errors.Is(openErr, unix.ELOOP) {
				return false, fmt.Errorf("%w: %s", errProjectSymlink, part)
			}
			if errors.Is(openErr, unix.ENOTDIR) {
				return false, fmt.Errorf("%w: %s", errProjectNotDirectory, part)
			}
			return false, openErr
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
		isDir := de.IsDir()
		isSymlink := de.Type()&os.ModeSymlink != 0
		readFn := func(maxBytes int64) ([]byte, error) {
			f, info, oErr := openRegularFileNoFollow(dirFD, name)
			if oErr != nil {
				if errors.Is(oErr, unix.ELOOP) {
					return nil, errProjectSymlink
				}
				if errors.Is(oErr, unix.EISDIR) {
					return nil, errProjectNotRegularFile
				}
				return nil, oErr
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
