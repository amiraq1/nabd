package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

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
func mcpEnvironment() []string {
	allowed := map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "LANG": true, "LC_ALL": true, "TERM": true, "PREFIX": true}
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if allowed[key] {
			env = append(env, entry)
		}
	}
	return env
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
func callExternalMCP(ctx context.Context, dir, command, tool string, args map[string]any) (output string, retErr error) {
	if (runtime.GOOS != "linux" && runtime.GOOS != "android") || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return "", fmt.Errorf("MCP process isolation unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	guard, err := mcpGuardPath()
	if err != nil {
		return "", err
	}
	callCtx, cancel := context.WithTimeout(ctx, mcpCallTimeout)
	defer cancel()
	if err := callCtx.Err(); err != nil {
		return "", err
	}
	process, stdin, stdout, err := startMCPProcess(callCtx, guard, dir, command)
	if err != nil {
		return "", err
	}
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
	return collectMCPText(result), nil
}
func collectMCPText(result *mcp.CallToolResult) string {
	var out strings.Builder
	truncated := false
	for _, content := range result.Content {
		tc, ok := content.(mcp.TextContent)
		if !ok {
			continue
		}
		text := tc.Text + "\n"
		remaining := maxMCPResponseBytes - out.Len()
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
