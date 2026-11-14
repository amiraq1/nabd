package tools

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestMCPTextIsCapped(t *testing.T) {
	result := &mcp.CallToolResult{Content: []mcp.Content{
		mcp.TextContent{Type: "text", Text: strings.Repeat("x", maxMCPResponseBytes*2)},
	}}
	out := collectMCPText(result)
	if len(out) > maxMCPResponseBytes+64 {
		t.Fatalf("output length %d exceeds the %d-byte cap", len(out), maxMCPResponseBytes)
	}
	if !strings.Contains(out, "mcp output truncated") {
		t.Fatalf("missing truncation marker")
	}
}
