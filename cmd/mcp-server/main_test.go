package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amiraq1/nabd/internal/tools"
	"github.com/mark3labs/mcp-go/mcp"
)

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	for _, content := range res.Content {
		if text, ok := content.(mcp.TextContent); ok {
			return text.Text
		}
	}
	t.Fatalf("tool result has no text content: %+v", res.Content)
	return ""
}

func TestServerIsReadOnlyAndDisablesMCPCall(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg, err := tools.NewRegistry(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	disableApprovalRequiredTools(reg)
	for _, forbidden := range []string{"edit_file", "go_verify", "mcp_call"} {
		if reg.Has(forbidden) {
			t.Errorf("approval-required tool %q is still registered", forbidden)
		}
	}
	advertised := make(map[string]bool)
	for _, def := range reg.OpenAITools() {
		fn, ok := def["function"].(map[string]any)
		if !ok {
			t.Fatalf("unexpected tool definition: %#v", def)
		}
		name, _ := fn["name"].(string)
		advertised[name] = true
		if name == "edit_file" || name == "go_verify" || name == "mcp_call" {
			t.Errorf("approval-required tool %q is still advertised", name)
		}
	}
	if !advertised["read_file"] || !advertised["list_dir"] {
		t.Fatalf("read-only tools missing from advertised tools: %v", advertised)
	}

	editReq := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "edit_file",
		Arguments: map[string]any{
			"path":     "victim.txt",
			"old_text": "original",
			"new_text": "changed",
		},
	}}
	res, err := makeHandler(reg, "edit_file")(context.Background(), editReq)
	if err != nil {
		t.Fatal(err)
	}
	if text := resultText(t, res); !strings.Contains(text, "unknown tool") {
		t.Fatalf("edit_file through MCP = %q, want disabled tool refusal", text)
	}
	if got, _ := os.ReadFile(victim); string(got) != "original" {
		t.Fatalf("MCP server modified the file without approval: %q", got)
	}

	readReq := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "read_file",
		Arguments: map[string]any{"path": "victim.txt"},
	}}
	res, err = makeHandler(reg, "read_file")(context.Background(), readReq)
	if err != nil {
		t.Fatal(err)
	}
	if text := resultText(t, res); text != "original" {
		t.Fatalf("read_file through MCP = %q", text)
	}
}

func TestMCPToolIncludesInputSchema(t *testing.T) {
	reg, err := tools.NewRegistry(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	var definition map[string]any
	for _, candidate := range reg.OpenAITools() {
		fn := candidate["function"].(map[string]any)
		if fn["name"] == "read_file" {
			definition = candidate
			break
		}
	}
	if definition == nil {
		t.Fatal("read_file definition not found")
	}
	tool, err := mcpToolFromOpenAIDef(definition)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.RawInputSchema, &schema); err != nil {
		t.Fatalf("decode MCP input schema %q: %v", tool.RawInputSchema, err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %s", tool.RawInputSchema)
	}
	for _, name := range []string{"path", "offset", "limit"} {
		if _, ok := properties[name]; !ok {
			t.Errorf("MCP schema is missing %q: %s", name, tool.RawInputSchema)
		}
	}
}
