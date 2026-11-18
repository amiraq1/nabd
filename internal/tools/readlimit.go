package tools

import (
	"fmt"
	"io"
	"os"
)

const listReadBatchSize = 256

// readBeforeLimitHook is a test-only seam; production leaves it nil.
var readBeforeLimitHook func()

// openRegular checks the path type before opening and rechecks the opened
// handle before any read. A static FIFO or device is rejected without being
// opened, so it cannot block the caller. A concurrent replacement between the
// Stat and Open calls can still block; this helper deliberately does not use
// O_NONBLOCK or platform-specific open flags.
func (r *Registry) openRegular(rel string, limit int64) (*os.File, int64, error) {
	info, err := r.root.Stat(rel)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%s is not a regular file", rel)
	}
	if limit > 0 && info.Size() > limit {
		return nil, 0, fmt.Errorf("%s is too large (%d bytes; limit %d)", rel, info.Size(), limit)
	}

	f, err := r.root.Open(rel)
	if err != nil {
		return nil, 0, err
	}
	openInfo, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	if !openInfo.Mode().IsRegular() {
		_ = f.Close()
		return nil, 0, fmt.Errorf("%s is not a regular file", rel)
	}
	if limit > 0 && openInfo.Size() > limit {
		_ = f.Close()
		return nil, 0, fmt.Errorf("%s is too large (%d bytes; limit %d)", rel, openInfo.Size(), limit)
	}
	if readBeforeLimitHook != nil {
		readBeforeLimitHook()
	}
	return f, openInfo.Size(), nil
}

// readFileLimited reads at most limit bytes and fails if the file grows past
// that limit. It returns exact bytes, so callers never hash or edit truncated
// content.
func (r *Registry) readFileLimited(rel string, limit int64) ([]byte, error) {
	f, _, err := r.openRegular(rel, limit)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeded the %d-byte read limit while being read", rel, limit)
	}
	return data, nil
}

type dirItem struct {
	name  string
	isDir bool
}

// dirHeap is a max-heap by name. It keeps the lexicographically smallest
// maxListEntries non-sensitive names while the directory is read in batches.
type dirHeap []dirItem

func (h dirHeap) Len() int           { return len(h) }
func (h dirHeap) Less(i, j int) bool { return h[i].name > h[j].name }
func (h dirHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *dirHeap) Push(x any)        { *h = append(*h, x.(dirItem)) }
func (h *dirHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
