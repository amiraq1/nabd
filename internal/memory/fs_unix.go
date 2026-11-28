//go:build unix

package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/amiraq1/nabd/internal/safefs"
	"golang.org/x/sys/unix"
)

type memoryDirHandle struct {
	nabdDir    *os.File
	memDir     *os.File
	createdMem bool
}

func (h *memoryDirHandle) close() {
	if h == nil {
		return
	}
	if h.memDir != nil {
		_ = h.memDir.Close()
		h.memDir = nil
	}
	if h.nabdDir != nil {
		_ = h.nabdDir.Close()
		h.nabdDir = nil
	}
}

type memoryLock struct {
	file *os.File
}

func (l *memoryLock) close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func directorySyncSupported() bool { return true }

func syncDirectoryFD(dir *os.File) error {
	if dir == nil {
		return nil
	}
	return dir.Sync()
}

func openMemoryDir(root *os.Root, create bool) (*memoryDirHandle, error) {
	if root == nil {
		return nil, errors.New("memory: nil root")
	}
	rootDir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer rootDir.Close()

	nabdDir, _, err := openOrCreateSubdirNoFollow(int(rootDir.Fd()), dirParent, create)
	if err != nil {
		return nil, err
	}
	memDir, createdMem, err := openOrCreateSubdirNoFollow(int(nabdDir.Fd()), dirName, create)
	if err != nil {
		_ = nabdDir.Close()
		return nil, err
	}
	return &memoryDirHandle{
		nabdDir:    nabdDir,
		memDir:     memDir,
		createdMem: createdMem,
	}, nil
}

func openOrCreateSubdirNoFollow(parentFD int, name string, create bool) (*os.File, bool, error) {
	d, err := safefs.OpenDirAtNoFollow(parentFD, name)
	if err == nil {
		return d, false, nil
	}
	if !create || !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	if mkErr := unix.Mkdirat(parentFD, name, 0o700); mkErr != nil && !errors.Is(mkErr, unix.EEXIST) {
		return nil, false, fmt.Errorf("memory: create directory %s: %w", name, mkErr)
	}
	d, err = safefs.OpenDirAtNoFollow(parentFD, name)
	if err != nil {
		return nil, false, err
	}
	return d, true, nil
}

func acquireMemoryLock(ctx context.Context, h *memoryDirHandle, wait time.Duration) (*memoryLock, error) {
	if h == nil || h.memDir == nil {
		return nil, errors.New("memory: nil directory handle")
	}
	fd, err := unix.Openat(
		int(h.memDir.Fd()),
		lockFileName,
		unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK,
		0o600,
	)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("memory: refusing symlinked %s: %w", lockFileName, err)
		}
		return nil, fmt.Errorf("memory: open %s: %w", lockFileName, err)
	}
	f := os.NewFile(uintptr(fd), dirParent+"/"+dirName+"/"+lockFileName)
	if f == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("memory: invalid descriptor for %s", lockFileName)
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("memory: stat %s: %w", lockFileName, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("memory: stat %s: %w", lockFileName, err)
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("memory: refusing non-regular %s", lockFileName)
	}
	if st.Nlink > 1 {
		_ = f.Close()
		return nil, fmt.Errorf("memory: refusing multiply-linked %s (nlink=%d)", lockFileName, st.Nlink)
	}

	deadline := time.Now().Add(wait)
	ticker := time.NewTicker(lockRetryInterval)
	defer ticker.Stop()

	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &memoryLock{file: f}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = f.Close()
			return nil, fmt.Errorf("memory: lock %s: %w", lockFileName, err)
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("memory: timed out after %s waiting for %s: %w", wait, lockFileName, ErrLockBusy)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func readEntriesFile(h *memoryDirHandle, maxBytes int64) ([]byte, error) {
	if h == nil || h.memDir == nil {
		return nil, errors.New("memory: nil directory handle")
	}
	f, info, err := safefs.OpenRegularAtNoFollow(int(h.memDir.Fd()), entriesFileName)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return nil, fmt.Errorf("memory: stat %s: %w", entriesFileName, err)
	}
	if st.Nlink > 1 {
		return nil, fmt.Errorf("%w: %s has link count %d", ErrCorruptStore, entriesFileName, st.Nlink)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes (%d bytes)", ErrLimitExceeded, entriesFileName, maxBytes, info.Size())
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("memory: read %s: %w", entriesFileName, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrLimitExceeded, entriesFileName, maxBytes)
	}
	return data, nil
}

func writeEntriesAtomic(
	h *memoryDirHandle,
	data []byte,
	syncDir func(*os.File) error,
	beforeRename func(),
	writeHook func(*os.File, []byte) error,
) error {
	if h == nil || h.memDir == nil {
		return errors.New("memory: nil directory handle")
	}
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Errorf("memory: temp file entropy: %w", err)
	}
	tmpName := ".tmp-" + hex.EncodeToString(rnd[:])
	memFD := int(h.memDir.Fd())

	fd, err := unix.Openat(
		memFD,
		tmpName,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0o600,
	)
	if err != nil {
		return fmt.Errorf("memory: create temp file: %w", err)
	}
	f := os.NewFile(uintptr(fd), tmpName)
	if f == nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(memFD, tmpName, 0)
		return errors.New("memory: invalid temp descriptor")
	}

	var writeErr error
	if writeHook != nil {
		writeErr = writeHook(f, data)
	} else {
		_, writeErr = f.Write(data)
	}
	modeErr := f.Chmod(0o600)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, modeErr, syncErr, closeErr); err != nil {
		_ = unix.Unlinkat(memFD, tmpName, 0)
		return fmt.Errorf("memory: write temp file: %w", err)
	}

	if beforeRename != nil {
		beforeRename()
	}
	if err := unix.Renameat(memFD, tmpName, memFD, entriesFileName); err != nil {
		_ = unix.Unlinkat(memFD, tmpName, 0)
		return fmt.Errorf("memory: replace %s: %w", entriesFileName, err)
	}
	if syncDir == nil {
		syncDir = syncDirectoryFD
	}
	if err := syncDir(h.memDir); err != nil {
		return &CommitUncertainError{Err: err}
	}
	if h.createdMem {
		if err := syncDir(h.nabdDir); err != nil {
			return &CommitUncertainError{Err: err}
		}
	}
	return nil
}
