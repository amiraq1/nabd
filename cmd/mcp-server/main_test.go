package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amiraq1/nabd/internal/tools"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

func newTestRegistry(t *testing.T) (*tools.Registry, string) {
	t.Helper()
	dir := t.TempDir()
	reg, err := tools.NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return reg, dir
}

func newTestClient(t *testing.T, reg *tools.Registry) *client.Client {
	t.Helper()
	tr := transport.NewInProcessTransport(newMCPServer(reg))
	c := client.NewClient(tr)
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("client.Start: %v", err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
		t.Fatalf("client.Initialize: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func listToolNames(t *testing.T, c *client.Client) map[string]bool {
	t.Helper()
	res, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}
	return names
}

func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, content := range res.Content {
		if tc, ok := content.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func TestF22ToolsListExcludesApprovalTools(t *testing.T) {
	reg, _ := newTestRegistry(t)
	c := newTestClient(t, reg)
	names := listToolNames(t, c)

	for _, forbidden := range []string{"edit_file", "go_verify", "mcp_call"} {
		if names[forbidden] {
			t.Errorf("tools/list exposes approval tool %q", forbidden)
		}
	}
	for _, allowed := range []string{"read_file", "list_dir"} {
		if !names[allowed] {
			t.Errorf("tools/list is missing %q", allowed)
		}
	}
}

func TestF22ConfirmationToolsRejectedByServer(t *testing.T) {
	reg, dir := newTestRegistry(t)
	c := newTestClient(t, reg)
	ctx := context.Background()

	call := func(name string, args map[string]any) {
		t.Helper()
		res, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
		if err == nil && (res == nil || !res.IsError) {
			t.Errorf("%s call was not rejected", name)
		}
	}

	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	call("edit_file", map[string]any{"path": "a.txt", "old_text": "old", "new_text": "new"})
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old\n" {
		t.Fatalf("edit_file changed the file: %q", got)
	}

	marker := filepath.Join(dir, "mcp-ran.txt")
	script := filepath.Join(dir, "probe.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	call("mcp_call", map[string]any{"command": script, "tool": "x"})
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("mcp_call started a process")
	}

	call("go_verify", map[string]any{"pkg": "./..."})
}

func TestF22HandlerRejectsApprovalTools(t *testing.T) {
	reg, dir := newTestRegistry(t)
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}

	handler := makeHandler(reg, "edit_file")
	res, err := handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "edit_file",
		Arguments: map[string]any{"path": "a.txt", "old_text": "old", "new_text": "new"},
	}})
	if err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("handler did not return an error result: %+v", res)
	}
	if text := resultText(res); !strings.Contains(text, "requires approval") {
		t.Fatalf("unexpected error text: %q", text)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old\n" {
		t.Fatalf("file changed: %q", got)
	}
}

func TestF22ReadOnlyToolsWork(t *testing.T) {
	reg, dir := newTestRegistry(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := newTestClient(t, reg)
	ctx := context.Background()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "read_file", Arguments: map[string]any{"path": "a.txt"}}})
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if res.IsError {
		t.Fatalf("read_file returned an error: %+v", res)
	}
	if text := resultText(res); !strings.Contains(text, "hello") {
		t.Fatalf("read_file content: %q", text)
	}

	res, err = c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "list_dir", Arguments: map[string]any{"path": "."}}})
	if err != nil {
		t.Fatalf("list_dir: %v", err)
	}
	if res.IsError {
		t.Fatalf("list_dir returned an error: %+v", res)
	}
	if text := resultText(res); !strings.Contains(text, "a.txt") {
		t.Fatalf("list_dir content: %q", text)
	}
}
