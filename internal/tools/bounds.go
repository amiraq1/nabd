package tools

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"
)

// boundedOutput drains all writes but retains only a bounded prefix and tail.
type boundedOutput struct {
	mu         sync.Mutex
	head, tail []byte
	total      int64
	cap        int
}

func newBoundedOutput(capacity int) *boundedOutput { return &boundedOutput{cap: capacity} }
func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.total += int64(n)
	headCap := b.cap / 2
	tailCap := b.cap - headCap
	if len(b.head) < headCap {
		take := headCap - len(b.head)
		if take > len(p) {
			take = len(p)
		}
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if len(p) >= tailCap {
		b.tail = append(b.tail[:0], p[len(p)-tailCap:]...)
	} else if len(p) > 0 {
		overflow := len(b.tail) + len(p) - tailCap
		if overflow > 0 {
			copy(b.tail, b.tail[overflow:])
			b.tail = b.tail[:len(b.tail)-overflow]
		}
		b.tail = append(b.tail, p...)
	}
	return n, nil
}
func (b *boundedOutput) String() string {
	head, omitted, tail := b.segments()
	if omitted <= 0 {
		return string(append(bytes.Clone(head), tail...))
	}
	return string(head) + fmt.Sprintf("\n... [%d bytes omitted] ...\n", omitted) + string(tail)
}

// segments returns the retained prefix, the number of dropped bytes between the
// prefix and the tail, and the retained tail without inserting any separator.
// Callers that must reason about the start and the end of a stream separately
// (for example a summary that looks for failure markers) use this instead of
// String, whose inline omission marker can itself be truncated away.
func (b *boundedOutput) segments() (head []byte, omitted int64, tail []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	omitted = b.total - int64(len(b.head)+len(b.tail))
	return bytes.Clone(b.head), omitted, bytes.Clone(b.tail)
}

// openRegularSingleLink checks before and after a nonblocking open on Unix.
// Checking the descriptor is essential because the path may change after Stat.
func openRegularSingleLink(root *os.Root, path string) (*os.File, error) {
	return openRegularSingleLinkWith(root, path, openWorkspaceFile)
}

// The opener is injectable so tests can exercise a replacement between Stat and Open.
func openRegularSingleLinkWith(root *os.Root, path string, open func(*os.Root, string) (*os.File, error)) (*os.File, error) {
	info, err := root.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing non-regular file: %s", path)
	}
	f, err := open(root, path)
	if err != nil {
		return nil, err
	}
	info, err = f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("refusing non-regular file: %s", path)
	}
	if err = ensureNoHardLinks(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// readBounded enforces the limit while reading, even when a file grows after stat.
func readBounded(root *os.Root, path string, limit int64) ([]byte, error) {
	f, err := openRegularSingleLink(root, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file too large (limit %d bytes): %s", limit, path)
	}
	if err := ensureNoHardLinks(f); err != nil {
		return nil, err
	}
	return data, nil
}
