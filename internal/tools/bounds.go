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
	b.mu.Lock()
	defer b.mu.Unlock()
	omitted := b.total - int64(len(b.head)+len(b.tail))
	if omitted <= 0 {
		return string(append(bytes.Clone(b.head), b.tail...))
	}
	return string(b.head) + fmt.Sprintf("\n... [%d bytes omitted] ...\n", omitted) + string(b.tail)
}

// readBounded enforces the limit while reading, even when a file grows after stat.
func readBounded(root *os.Root, path string, limit int64) ([]byte, error) {
	f, err := root.Open(path)
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
	return data, nil
}
