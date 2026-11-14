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

func mcpToolFromOpenAIDef(def map[string]any) (mcp.Tool, error) {
	fn, ok := def["function"].(map[string]any)
	if !ok {
		return mcp.Tool{}, fmt.Errorf("invalid OpenAI tool definition")
	}
	name, nameOK := fn["name"].(string)
	desc, descOK := fn["description"].(string)
	if !nameOK || !descOK {
		return mcp.Tool{}, fmt.Errorf("tool name or description is missing")
	}
	schema, err := json.Marshal(fn["parameters"])
	if err != nil {
		return mcp.Tool{}, fmt.Errorf("encode input schema for %s: %w", name, err)
	}
	return mcp.NewToolWithRawSchema(name, desc, schema), nil
}

func disableApprovalRequiredTools(reg *tools.Registry) {
	for _, definition := range reg.OpenAITools() {
		fn, ok := definition["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		if name != "" && reg.RequiresApproval(name) {
			reg.Disable(name)
		}
	}
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get cwd: %v\n", err)
		os.Exit(1)
	}

	reg, err := tools.NewRegistry(cwd, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create registry: %v\n", err)
		os.Exit(1)
	}
	defer reg.Close()
	// This server has no interactive approver, so advertise read-only tools only.
	disableApprovalRequiredTools(reg)

	s := server.NewMCPServer(
		"NabdCoder Tools",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	// Register tools from the registry
	for _, toolDef := range reg.OpenAITools() {
		tool, err := mcpToolFromOpenAIDef(toolDef)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to register MCP tool: %v\n", err)
			os.Exit(1)
		}
		s.AddTool(tool, makeHandler(reg, tool.Name))
	}

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		os.Exit(1)
	}
}

func makeHandler(reg *tools.Registry, name string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, _ := json.Marshal(request.GetArguments())
		result := reg.Run(ctx, name, args)
		return mcp.NewToolResultText(result), nil
	}
}
