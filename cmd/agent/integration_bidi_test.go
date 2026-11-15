package main

import (
	"strings"
	"testing"
)

func TestIntegrationBidiControlsAreVisible(t *testing.T) {
	input := "مرحبا\u202eabc\u2066def\u061cنص"
	got := sanitizeForDisplay(input)
	if got != `مرحبا\u202Eabc\u2066def\u061Cنص` {
		t.Fatalf("unexpected sanitization: %q", got)
	}
}
func TestIntegrationBidiApprovalFailsClosed(t *testing.T) {
	_, m := testModel(t)
	tc := testToolCall("mcp_call", `{"command":"safe\u202Eevil","tool":"test"}`)
	_, err := prepareApprovalPreview(m.registry, tc)
	if err == nil || !strings.Contains(err.Error(), "command") {
		t.Fatalf("hidden direction control did not block approval: %v", err)
	}
}
