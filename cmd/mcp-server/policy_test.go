package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amiraq1/nabd/internal/tools"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func testClient(t *testing.T, s *server.MCPServer) *client.Client {
	t.Helper()
	c, err := client.NewInProcessClient(s)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.ProtocolVersion20251125
	init.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "1"}
	if _, err = c.Initialize(context.Background(), init); err != nil {
		t.Fatal(err)
	}
	return c
}
func call(t *testing.T, c *client.Client, name string, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	return c.CallTool(context.Background(), req)
}
func TestPolicyMCP(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "spawned")
	f := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(f, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "probe")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// go_verify runs an executable named go: detect any invocation.
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	reg, err := tools.NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(reg)
	c := testClient(t, s)
	listed, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	for _, name := range []string{"edit_file", "go_verify", "mcp_call"} {
		if names[name] {
			t.Errorf("%s exposed", name)
		}
	}
	for _, name := range []string{"read_file", "list_dir"} {
		if !names[name] {
			t.Errorf("%s missing", name)
		}
	}
	args := map[string]map[string]any{
		"edit_file": {"path": "sample.txt", "old_text": "before", "new_text": "after"},
		"go_verify": {"pkg": "."},
		"mcp_call":  {"command": script, "tool": "anything"},
	}
	for name, a := range args {
		result, err := call(t, c, name, a)
		if err == nil && !result.IsError {
			t.Errorf("%s accepted: %v", name, result)
		}
	}
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"read_file", map[string]any{"path": "sample.txt"}, "before"},
		{"list_dir", map[string]any{"path": "."}, "sample.txt"},
	} {
		result, err := call(t, c, tc.name, tc.args)
		if err != nil || result == nil || result.IsError || !strings.Contains(fmt.Sprint(result.Content), tc.want) {
			t.Errorf("%s failed: %v %v", tc.name, result, err)
		}
	}
	// Simulate accidental future registration: the handler itself must refuse.
	for name, a := range args {
		s.AddTool(mcp.NewTool(name), makeHandler(reg, name))
		result, err := call(t, c, name, a)
		if err == nil && !result.IsError {
			t.Errorf("registered unsafe %s accepted: %v", name, result)
		}
	}
	data, err := os.ReadFile(f)
	if err != nil || string(data) != "before" {
		t.Errorf("file changed: %q %v", data, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("process spawned: %v", err)
	}
}
