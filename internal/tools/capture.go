package tools

import (
	"strings"
	"sync"
)

const (
	maxGoVerifyCapture      = 128 << 10
	goVerifyCaptureHead     = 64 << 10
	goVerifyCaptureTail     = 64 << 10
	captureTruncationMarker = "\n...[go_verify capture truncated; middle output discarded]...\n"
)

// boundedCapture keeps at most the first 64 KiB and the last 64 KiB of a
// stream. Calls to Write never block and never fail; bytes in the middle are
// dropped and Truncated reports that loss.
type boundedCapture struct {
	mu       sync.Mutex
	head     []byte
	tail     []byte
	tailPos  int
	tailFull bool
	total    int64
}

func newBoundedCapture() *boundedCapture {
	return &boundedCapture{tail: make([]byte, goVerifyCaptureTail)}
}

func (c *boundedCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	n := len(p)
	c.total += int64(n)

	if len(c.head) < goVerifyCaptureHead {
		take := goVerifyCaptureHead - len(c.head)
		if take > len(p) {
			take = len(p)
		}
		c.head = append(c.head, p[:take]...)
		p = p[take:]
	}

	for len(p) > 0 {
		space := len(c.tail) - c.tailPos
		if space > len(p) {
			space = len(p)
		}
		copy(c.tail[c.tailPos:], p[:space])
		c.tailPos += space
		if c.tailPos == len(c.tail) {
			c.tailPos = 0
			c.tailFull = true
		}
		p = p[space:]
	}
	return n, nil
}

func (c *boundedCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	tail := c.tailBytesLocked()
	if c.total <= int64(len(c.head)+len(tail)) {
		return string(c.head) + string(tail)
	}
	return string(c.head) + captureTruncationMarker + string(tail)
}

func (c *boundedCapture) Truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total > int64(maxGoVerifyCapture)
}

func (c *boundedCapture) tailBytesLocked() []byte {
	if !c.tailFull {
		return c.tail[:c.tailPos]
	}
	if c.tailPos == 0 {
		return c.tail
	}
	out := make([]byte, 0, len(c.tail))
	out = append(out, c.tail[c.tailPos:]...)
	out = append(out, c.tail[:c.tailPos]...)
	return out
}

func summarizeGoTestCapture(status, output string, err error, truncated bool) string {
	summary := summarizeGoTest(status, output, err)
	if !truncated {
		return summary
	}
	const notice = "[capture truncated: retained first 64 KiB and last 64 KiB; middle output discarded]"
	if i := strings.IndexByte(summary, '\n'); i >= 0 {
		return summary[:i+1] + "  " + notice + "\n" + summary[i+1:]
	}
	return summary + "\n  " + notice
}
