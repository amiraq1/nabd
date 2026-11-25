package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestREAMCPTextCapBoundariesUTF8 verifies the 32 KiB response ceiling for REA
// and ensures boundary truncation preserves valid UTF-8 and marks truncation.
func TestREAMCPTextCapBoundariesUTF8(t *testing.T) {
	// Test at exact limits around 32 KiB
	for _, n := range []int{maxREAMCPResponseBytes - 1, maxREAMCPResponseBytes, maxREAMCPResponseBytes + 1} {
		result := &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{Type: "text", Text: strings.Repeat("a", n)},
			},
		}
		out := collectMCPTextWithLimit(result, maxREAMCPResponseBytes)
		if len(out) > maxREAMCPResponseBytes+64 {
			t.Fatalf("length %d exceeded maxREAMCPResponseBytes+64 for input size %d", len(out), n)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8 string returned for size %d", n)
		}
		if n >= maxREAMCPResponseBytes && !strings.Contains(out, "[mcp output truncated]") {
			t.Fatalf("expected truncation marker for size %d", n)
		}
	}

	// Test 64 KiB oversized output
	largeResult := &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: strings.Repeat("X", 64<<10)},
		},
	}
	largeOut := collectMCPTextWithLimit(largeResult, maxREAMCPResponseBytes)
	if len(largeOut) > maxREAMCPResponseBytes+64 {
		t.Fatalf("large output length %d exceeded 32 KiB limit", len(largeOut))
	}
	if !strings.Contains(largeOut, "[mcp output truncated]") {
		t.Fatal("expected truncation marker for 64 KiB input")
	}
	if !utf8.ValidString(largeOut) {
		t.Fatal("large output broke UTF-8 encoding")
	}

	// Multibyte Arabic string cut at boundary
	arabicResult := &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{
				Type: "text",
				Text: strings.Repeat("x", maxREAMCPResponseBytes-1) + "هندسة عكسية",
			},
		},
	}
	arabicOut := collectMCPTextWithLimit(arabicResult, maxREAMCPResponseBytes)
	if !utf8.ValidString(arabicOut) {
		t.Fatal("multibyte Arabic boundary truncation broke UTF-8 encoding")
	}
}

// TestREAMCPResponseCapDifferentiation verifies that standard MCP servers retain
// an 8 KiB limit while the REA MCP server receives the dedicated 32 KiB limit.
func TestREAMCPResponseCapDifferentiation(t *testing.T) {
	if maxMCPResponseBytes != 8192 {
		t.Fatalf("expected standard maxMCPResponseBytes to remain 8192, got %d", maxMCPResponseBytes)
	}
	if maxREAMCPResponseBytes != 32768 {
		t.Fatalf("expected maxREAMCPResponseBytes to be 32768, got %d", maxREAMCPResponseBytes)
	}

	input := strings.Repeat("B", 16384) // 16 KiB
	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: input},
		},
	}

	// Standard limit truncates 16 KiB
	stdOut := collectMCPTextWithLimit(result, maxMCPResponseBytes)
	if !strings.Contains(stdOut, "[mcp output truncated]") {
		t.Fatal("expected 16 KiB input to be truncated under 8 KiB standard limit")
	}

	// REA limit does not truncate 16 KiB
	reaOut := collectMCPTextWithLimit(result, maxREAMCPResponseBytes)
	if strings.Contains(reaOut, "[mcp output truncated]") {
		t.Fatal("expected 16 KiB input NOT to be truncated under 32 KiB REA limit")
	}
}

// TestREAMCPRequiresApproval verifies that mcp_call always requires explicit approval.
func TestREAMCPRequiresApproval(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	if !reg.RequiresApproval("mcp_call") {
		t.Fatal("mcp_call must always require user approval")
	}
	if reg.RequiresApproval("read_file") {
		t.Fatal("read_file should not require approval")
	}
}

// TestREAMCPUntrustedOutputAuditSanitization ensures that potentially malicious
// control characters and bidirectional overrides in tool outputs/details are escaped.
func TestREAMCPUntrustedOutputAuditSanitization(t *testing.T) {
	maliciousDetail := "binary_analysis\x00\r\n\u202EReversed\u200C"
	sanitized := sanitizeAuditDetail(maliciousDetail)

	if strings.Contains(sanitized, "\x00") {
		t.Fatalf("NUL byte survived audit sanitization: %q", sanitized)
	}
	if strings.Contains(sanitized, "\u202E") {
		t.Fatalf("Bidi override survived audit sanitization without escaping: %q", sanitized)
	}
	if !strings.Contains(sanitized, "\\u0000") {
		t.Fatalf("expected NUL to be escaped as \\u0000, got: %q", sanitized)
	}
	if !strings.Contains(sanitized, "\\u202E") {
		t.Fatalf("expected Bidi override to be escaped as \\u202E, got: %q", sanitized)
	}
	if !strings.Contains(sanitized, "binary_analysis") {
		t.Fatalf("expected text content binary_analysis to be preserved: %q", sanitized)
	}
}

// TestREAMCPRegistryOptInEnabledAndDisabled tests enabling and disabling REA on the registry.
func TestREAMCPRegistryOptInEnabledAndDisabled(t *testing.T) {
	dir := t.TempDir()

	// 1. Disabled by default
	regDef, err := NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = regDef.Close() })

	if regDef.IsREAEnabled() {
		t.Fatal("REA should be disabled by default")
	}
	bin, target := regDef.READetails()
	if bin != "" || target != "" {
		t.Fatalf("expected empty READetails when disabled, got (%q, %q)", bin, target)
	}

	// 2. Enabled with mock bin, mock guard, and target dir
	binFile := filepath.Join(dir, "mock-rea")
	binContent := []byte("#!/bin/sh\necho rea-mock\n")
	if err := os.WriteFile(binFile, binContent, 0o755); err != nil {
		t.Fatal(err)
	}
	binHash := sha256.Sum256(binContent)
	binHashHex := hex.EncodeToString(binHash[:])

	guardFile := filepath.Join(dir, "mock-guard")
	guardContent := []byte("#!/bin/sh\nexec \"$@\"\n")
	if err := os.WriteFile(guardFile, guardContent, 0o755); err != nil {
		t.Fatal(err)
	}
	guardHash := sha256.Sum256(guardContent)
	guardHashHex := hex.EncodeToString(guardHash[:])

	targetDir := filepath.Join(dir, "samples")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv(REAMCPBinEnv, binFile)
	t.Setenv(REAMCPSHA256Env, binHashHex)
	t.Setenv(REAMCPGuardEnv, guardFile)
	t.Setenv(REAMCPGuardSHA256Env, guardHashHex)
	t.Setenv(REATargetDirEnv, targetDir)

	regOpt, err := NewRegistry(dir)
	if err != nil {
		t.Fatalf("failed to create registry with REA enabled: %v", err)
	}
	t.Cleanup(func() { _ = regOpt.Close() })

	if !regOpt.IsREAEnabled() {
		t.Fatal("expected REA to be enabled on regOpt")
	}
	gotBin, gotTarget := regOpt.READetails()
	if gotBin != binFile || gotTarget != targetDir {
		t.Fatalf("expected (%q, %q), got (%q, %q)", binFile, targetDir, gotBin, gotTarget)
	}
}
