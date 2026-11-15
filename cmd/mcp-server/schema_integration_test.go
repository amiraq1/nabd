package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/amiraq1/nabd/internal/tools"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestIntegrationMCPToolDefinitionFailsClosed(t *testing.T) {
	valid := func() map[string]any {
		return map[string]any{"function": map[string]any{"name": "read_file", "description": "Read", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}}
	}
	tests := []struct {
		name string
		def  map[string]any
	}{
		{"missing function", map[string]any{}},
		{"wrong function", map[string]any{"function": "no"}},
		{"missing name", map[string]any{"function": map[string]any{"description": "Read", "parameters": map[string]any{"type": "object"}}}},
		{"empty name", map[string]any{"function": map[string]any{"name": "", "description": "Read", "parameters": map[string]any{"type": "object"}}}},
		{"missing description", map[string]any{"function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}},
		{"missing schema", map[string]any{"function": map[string]any{"name": "read_file", "description": "Read"}}},
		{"non-object schema", map[string]any{"function": map[string]any{"name": "read_file", "description": "Read", "parameters": map[string]any{"type": "string"}}}},
		{"unencodable schema", map[string]any{"function": map[string]any{"name": "read_file", "description": "Read", "parameters": map[string]any{"type": "object", "invalid": make(chan int)}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() != nil {
					t.Error("malformed definition caused panic")
				}
			}()
			if _, err := mcpToolFromOpenAIDef(tc.def); err == nil {
				t.Error("malformed definition was accepted")
			}
		})
	}
	tool, err := mcpToolFromOpenAIDef(valid())
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(tool.RawInputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || properties["path"] == nil {
		t.Fatal("path schema lost")
	}
}

func TestIntegrationMCPListsActualInputSchemas(t *testing.T) {
	reg, err := tools.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	s, err := newServer(reg)
	if err != nil {
		t.Fatal(err)
	}
	c := testClient(t, s)
	result, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range result.Tools {
		if tool.Name == "read_file" {
			for _, name := range []string{"path", "offset", "limit", "format"} {
				if _, ok := tool.InputSchema.Properties[name]; !ok {
					t.Errorf("read_file schema missing %s", name)
				}
			}
			return
		}
	}
	t.Fatal("read_file not advertised")
}
