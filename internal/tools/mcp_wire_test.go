package tools

import (
	"bytes"
	"errors"
	"github.com/mark3labs/mcp-go/mcp"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestF24WireReaderBoundaries(t *testing.T) {
	for _, n := range []int{(1 << 20) - 1, 1 << 20, (1 << 20) + 1} {
		r := &wireLimitReader{r: bytes.NewReader(make([]byte, n)), remaining: maxMCPWireBytes}
		out, err := io.ReadAll(r)
		if n <= maxMCPWireBytes {
			if err != nil || len(out) != n || r.exceeded.Load() {
				t.Fatalf("boundary %d rejected", n)
			}
		} else if !errors.Is(err, errMCPResponseTooLarge) || len(out) != maxMCPWireBytes || !r.exceeded.Load() {
			t.Fatalf("overflow %d accepted", n)
		}
	}
}
func TestF24TextCapBoundariesUTF8(t *testing.T) {
	for _, n := range []int{maxMCPResponseBytes - 1, maxMCPResponseBytes, maxMCPResponseBytes + 1} {
		result := &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Type: "text", Text: strings.Repeat("x", n)}}}
		out := collectMCPText(result)
		if len(out) > maxMCPResponseBytes+64 || !utf8.ValidString(out) {
			t.Fatal("invalid cap")
		}
		if n >= maxMCPResponseBytes && !strings.Contains(out, "truncated") {
			t.Fatal("overflow marker missing")
		}
	}
	result := &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Type: "text", Text: strings.Repeat("x", maxMCPResponseBytes-1) + "مرحبا"}}}
	if !utf8.ValidString(collectMCPText(result)) {
		t.Fatal("UTF-8 truncation broke encoding")
	}
}
