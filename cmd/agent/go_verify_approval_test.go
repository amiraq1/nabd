package main

import (
	"strings"
	"testing"

	"github.com/amiraq1/nabd/internal/tools"
)

func TestGoVerifyApprovalShowsPackage(t *testing.T) {
	reg, err := tools.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if !reg.NeedsApproval("go_verify") {
		t.Fatal("go_verify must require approval")
	}
	m := initialModel(reg)
	tc := ToolCall{ID: "call_go", Type: "function"}
	tc.Function.Name = "go_verify"
	tc.Function.Arguments = `{"pkg":"./internal/tools"}`
	m.pendingTools = []ToolCall{tc}
	m.awaitingApproval = true

	view := m.View()
	if !strings.Contains(view, "./internal/tools") {
		t.Fatalf("approval prompt does not show pkg: %q", view)
	}
}
