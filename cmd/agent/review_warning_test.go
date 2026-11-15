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
