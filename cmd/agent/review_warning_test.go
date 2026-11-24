package main

import (
	"strings"
	"testing"
)

func TestReviewExecutionApprovalWarnings(t *testing.T) {
	_, m := testModel(t)
	tests := []struct{ name, args string }{{"mcp_call", `{"command":"example","tool":"x"}`}, {"go_verify", `{"pkg":"./..."}`}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := prepareApprovalPreview(m.registry, testToolCall(tc.name, tc.args))
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range []string{"filesystem", "network", "user permissions"} {
				if !strings.Contains(strings.ToLower(p.Summary), s) {
					t.Errorf("warning lacks %q", s)
				}
			}
		})
	}
}

func TestReviewMCPApprovalWarnsExecutableCanChange(t *testing.T) {
	_, m := testModel(t)
	p, err := prepareApprovalPreview(m.registry, testToolCall("mcp_call", `{"command":"example","tool":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(p.Summary), "configured server") ||
		!strings.Contains(strings.ToLower(p.Summary), "verification window") {
		t.Fatal("missing identity-bound or residual-TOCTOU warning")
	}
}

func TestReviewMCPWarningNotClipped(t *testing.T) {
	_, m := testModel(t)
	for _, n := range []int{1000, 1300, 1500} {
		args := `{"command":"` + strings.Repeat("x", n) + `","tool":"x"}`
		p, err := prepareApprovalPreview(m.registry, testToolCall("mcp_call", args))
		if err == nil && (!strings.Contains(p.Summary, mcpIdentityWarning) || !strings.Contains(p.Summary, "filesystem")) {
			t.Fatal("approval allowed with clipped warnings")
		}
	}
}
