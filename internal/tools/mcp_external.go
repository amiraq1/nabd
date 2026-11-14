package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

type wireLimitReader struct {
	r         io.Reader
	remaining int64
	exceeded  bool
}

func (l *wireLimitReader) Read(p []byte) (int, error) {
	if l.exceeded {
		return 0, errMCPResponseTooLarge
	}
	if l.remaining <= 0 {
		l.exceeded = true
		return 0, errMCPResponseTooLarge
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.r.Read(p)
	l.remaining -= int64(n)
	return n, err
}

func mcpEnvironment() []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true,
		"LANG": true, "LC_ALL": true, "TERM": true, "PREFIX": true,
	}
	env := []string{}
	for _, entry := range os.Environ() {
		key := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			key = entry[:i]
		}
		if allowed[key] {
			env = append(env, entry)
		}
	}
	return env
}

func mcpGuardPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("NABD_MCP_GUARD")); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("MCP process isolation unavailable: %s: %w", p, err)
		}
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	p := filepath.Join(filepath.Dir(exe), "nabd-mcp-guard")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("MCP process isolation unavailable: install nabd-mcp-guard next to the agent (%s)", p)
	}
	return p, nil
}

func callExternalMCP(ctx context.Context, dir, command, tool string, args map[string]any) (string, error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "android" {
		return "", fmt.Errorf("MCP process isolation is unsupported on %s", runtime.GOOS)
	}
	guard, err := mcpGuardPath()
	if err != nil {
		return "", err
	}

	callCtx, cancel := context.WithTimeout(ctx, mcpCallTimeout)
	defer cancel()

	cmd := exec.CommandContext(callCtx, guard, command)
	cmd.Dir = dir
	cmd.Env = mcpEnvironment()
	cmd.Stderr = nil
	prepareProcess(cmd)
	cmd.WaitDelay = 5 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return "", err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return "", fmt.Errorf("failed to start MCP guard: %w", err)
	}

	waitCh := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		killProcessGroup(cmd)
		close(waitCh)
		cancel()
	}()

	limited := &wireLimitReader{r: stdout, remaining: maxMCPWireBytes}
	c := client.NewClient(transport.NewIO(limited, stdin, nil))
	defer func() {
		killProcessGroup(cmd)
		_ = c.Close()
		_ = stdin.Close()
		_ = stdout.Close()
		cancel()
		select {
		case <-waitCh:
		case <-time.After(5 * time.Second):
		}
	}()

	fail := func(err error) (string, error) {
		if limited.exceeded {
			return "", errMCPResponseTooLarge
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
	if limited.exceeded {
		return "", errMCPResponseTooLarge
	}
	return collectMCPText(result), nil
}

func collectMCPText(result *mcp.CallToolResult) string {
	var out strings.Builder
	for _, content := range result.Content {
		tc, ok := content.(mcp.TextContent)
		if !ok {
			continue
		}
		if out.Len() >= maxMCPResponseBytes {
			out.WriteString("\n...[mcp output truncated]...\n")
			break
		}
		remaining := maxMCPResponseBytes - out.Len()
		text := tc.Text
		if len(text) > remaining {
			text = text[:remaining]
			for len(text) > 0 && !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
			out.WriteString(text)
			out.WriteString("\n...[mcp output truncated]...\n")
			break
		}
		out.WriteString(text)
		out.WriteString("\n")
	}
	if out.Len() == 0 {
		return "(no text content returned)"
	}
	return out.String()
}
