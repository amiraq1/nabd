package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/amiraq1/nabd/internal/sandbox"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	maxMCPWireBytes     = 1 << 20
	maxMCPResponseBytes = 8192
	mcpCallTimeout      = 60 * time.Second
)

var errMCPResponseTooLarge = errors.New("MCP response exceeded the 1 MiB wire limit")

// Only tests set this; no environment variable can substitute the guard.
//
//lint:ignore U1000 exercised by platform-specific integration tests
var mcpGuardOverride string

type wireLimitReader struct {
	r         io.Reader
	remaining int64
	exceeded  atomic.Bool
}

func (l *wireLimitReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if l.exceeded.Load() {
		return 0, errMCPResponseTooLarge
	}
	if l.remaining == 0 {
		var probe [1]byte
		n, err := l.r.Read(probe[:])
		if n > 0 {
			l.exceeded.Store(true)
			return 0, errMCPResponseTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.r.Read(p)
	l.remaining -= int64(n)
	return n, err
}

func mcpGuardPath() (string, error) {
	path := mcpGuardOverride
	if path == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		path = filepath.Join(filepath.Dir(exe), "nabd-mcp-guard")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("MCP process isolation unavailable: install nabd-mcp-guard next to the agent")
	}
	return path, nil
}

// callExternalMCPServer launches a user-preconfigured server inside the proven
// sandbox. The model cannot choose an executable or an environment variable.
func callExternalMCPServer(ctx context.Context, dir string, b sandbox.Backend, server MCPServer, tool string, args map[string]any) (output string, retErr error) {
	if b == nil {
		return "", fmt.Errorf("mcp_call is disabled: no proven execution sandbox is active")
	}
	guard := server.Config.Guard
	targetArgs := append([]string{server.Config.Executable}, server.Config.Args...)
	outer := guard
	if server.Config.UserGuard != "" {
		// A user guard is an outer wrapper: it receives the built-in guard
		// command and its arguments, then the configured server command.
		outer = server.Config.UserGuard
		targetArgs = append([]string{guard}, append(targetArgs, server.Config.UserGuardArgs...)...)
	}
	maskedDirs, maskedFiles, err := collectSensitiveWorkspaceMasks(dir)
	if err != nil {
		return "", err
	}
	profile := sandbox.Profile{
		WorkDir:            dir,
		WritableWorkspace:  false,
		MaskedDirs:         maskedDirs,
		MaskedFiles:        maskedFiles,
		Env:                MCPEnvironment(server.Config),
		MemoryLimitBytes:   sandbox.DefaultMemoryLimit(),
		ProcessLimit:       32,
		ExtraReadonlyBinds: append([]string{guard, server.Config.Executable}, server.Config.ExtraReadonlyBinds...),
	}
	if server.Config.UserGuard != "" {
		profile.ExtraReadonlyBinds = append(profile.ExtraReadonlyBinds, server.Config.UserGuard)
	}
	callCtx, cancel := context.WithTimeout(ctx, mcpCallTimeout)
	defer cancel()
	if err := callCtx.Err(); err != nil {
		return "", err
	}
	cmd, err := b.Wrap(callCtx, profile, outer, targetArgs...)
	if err != nil {
		return "", err
	}
	process, stdin, stdout, err := startMCPProcessWithCmd(callCtx, cmd)
	if err != nil {
		return "", err
	}
	return runMCPServerProtocol(callCtx, process, stdin, stdout, server.Name, tool, args)
}

func runMCPServerProtocol(callCtx context.Context, process *mcpProcess, stdin, stdout *os.File, serverName, tool string, args map[string]any) (output string, retErr error) {
	limited := &wireLimitReader{r: stdout, remaining: maxMCPWireBytes}
	c := client.NewClient(transport.NewIO(limited, stdin, nil))
	defer func() {
		process.terminate()
		_ = c.Close()
		_ = stdin.Close()
		_ = stdout.Close()
		if cleanupErr := process.closeAndWait(); cleanupErr != nil {
			output = ""
			retErr = errors.Join(retErr, cleanupErr)
		}
	}()
	fail := func(err error) (string, error) {
		if limited.exceeded.Load() {
			return "", errMCPResponseTooLarge
		}
		if errors.Is(callCtx.Err(), context.Canceled) {
			return "", fmt.Errorf("MCP call canceled: %w", callCtx.Err())
		}
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("MCP call timed out: %w", callCtx.Err())
		}
		return "", err
	}
	if err := c.Start(callCtx); err != nil {
		return fail(fmt.Errorf("failed to start MCP client: %w", err))
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.ProtocolVersion20251125
	init.Params.ClientInfo = mcp.Implementation{Name: "NabdCoder", Version: "1"}
	if _, err := c.Initialize(callCtx, init); err != nil {
		return fail(fmt.Errorf("MCP initialization failed: %w", err))
	}
	result, err := c.CallTool(callCtx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: tool, Arguments: args}})
	if err != nil {
		return fail(fmt.Errorf("mcp call failed: %w", err))
	}
	if limited.exceeded.Load() {
		return "", errMCPResponseTooLarge
	}
	if err := callCtx.Err(); err != nil {
		return fail(err)
	}
	maxBytes := maxMCPResponseBytes
	if serverName == REAServerName {
		maxBytes = maxREAMCPResponseBytes
	}
	return collectMCPTextWithLimit(result, maxBytes), nil
}

func collectMCPTextWithLimit(result *mcp.CallToolResult, maxBytes int) string {
	var out strings.Builder
	truncated := false
	for _, content := range result.Content {
		tc, ok := content.(mcp.TextContent)
		if !ok {
			continue
		}
		text := tc.Text + "\n"
		remaining := maxBytes - out.Len()
		if len(text) > remaining {
			text = text[:remaining]
			for len(text) > 0 && !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
			out.WriteString(text)
			truncated = true
			break
		}
		out.WriteString(text)
	}
	if truncated {
		out.WriteString("\n...[mcp output truncated]...\n")
	}
	if out.Len() == 0 {
		return "(no text content returned)"
	}
	return out.String()
}

func collectMCPText(result *mcp.CallToolResult) string {
	return collectMCPTextWithLimit(result, maxMCPResponseBytes)
}
