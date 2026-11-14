package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/amiraq1/nabd/internal/tools"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get cwd: %v\n", err)
		os.Exit(1)
	}

	reg, err := tools.NewRegistry(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create registry: %v\n", err)
		os.Exit(1)
	}

	s := newMCPServer(reg)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		os.Exit(1)
	}
}

func newMCPServer(reg *tools.Registry) *server.MCPServer {
	s := server.NewMCPServer(
		"NabdCoder Tools",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	for _, toolDef := range reg.OpenAITools() {
		fn := toolDef["function"].(map[string]any)
		name := fn["name"].(string)
		if reg.NeedsApproval(name) {
			continue
		}
		desc := fn["description"].(string)
		tool := mcp.NewTool(name, mcp.WithDescription(desc))
		s.AddTool(tool, makeHandler(reg, name))
	}
	return s
}

func makeHandler(reg *tools.Registry, name string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if reg.NeedsApproval(name) {
			return mcp.NewToolResultError(fmt.Sprintf("tool %q requires approval and is not available over MCP", name)), nil
		}
		args, _ := json.Marshal(request.GetArguments())
		result := reg.Run(ctx, name, args)
		return mcp.NewToolResultText(result), nil
	}
}
