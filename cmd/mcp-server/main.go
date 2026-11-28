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

// mcpToolFromOpenAIDef validates registry definitions instead of relying on
// unchecked assertions; malformed definitions stop the server at startup.
func mcpToolFromOpenAIDef(def map[string]any) (mcp.Tool, error) {
	fn, ok := def["function"].(map[string]any)
	if !ok {
		return mcp.Tool{}, fmt.Errorf("invalid OpenAI tool definition")
	}
	name, ok := fn["name"].(string)
	if !ok || name == "" {
		return mcp.Tool{}, fmt.Errorf("tool name is missing")
	}
	desc, ok := fn["description"].(string)
	if !ok {
		return mcp.Tool{}, fmt.Errorf("description missing for tool %s", name)
	}
	params, ok := fn["parameters"].(map[string]any)
	if !ok || params["type"] != "object" {
		return mcp.Tool{}, fmt.Errorf("invalid input schema for tool %s", name)
	}
	schema, err := json.Marshal(params)
	if err != nil {
		return mcp.Tool{}, fmt.Errorf("encode input schema for tool %s: %w", name, err)
	}
	return mcp.NewToolWithRawSchema(name, desc, schema), nil
}

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
	defer reg.Close()

	s, err := newServer(reg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to register MCP tool: %v\n", err)
		os.Exit(1)
	}
	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		os.Exit(1)
	}
}

func isMCPDeniedTool(reg *tools.Registry, name string) bool {
	if reg.RequiresApproval(name) {
		return true
	}
	// Workspace memory tools are excluded from MCP by default because MCP clients
	// bypass Nabd's interactive disclosure and secret-review gate.
	return name == "memory_search" || name == "memory_save"
}

func newServer(reg *tools.Registry) (*server.MCPServer, error) {
	s := server.NewMCPServer(
		"NabdCoder Tools",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	// Register tools from the registry
	for _, toolDef := range reg.OpenAITools() {
		tool, err := mcpToolFromOpenAIDef(toolDef)
		if err != nil {
			return nil, err
		}
		if isMCPDeniedTool(reg, tool.Name) {
			continue
		}
		s.AddTool(tool, makeHandler(reg, tool.Name))
	}

	return s, nil
}

func makeHandler(reg *tools.Registry, name string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if isMCPDeniedTool(reg, name) {
			return mcp.NewToolResultError("tool requires approval or is unavailable via MCP"), nil
		}
		args, _ := json.Marshal(request.GetArguments())
		result := reg.Run(ctx, name, args)
		return mcp.NewToolResultText(result), nil
	}
}
