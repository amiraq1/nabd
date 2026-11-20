package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

func buildMCPBinary(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	binPath := filepath.Join(dir, "mcp-server.exe")
	cmd := exec.Command("go", "build", "-o", binPath, "../../cmd/mcp-server")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build mcp-server: %v\n%s", err, string(out))
	}
	return binPath
}

func BenchmarkMCP_ColdVsWarm(b *testing.B) {
	serverBin := buildMCPBinary(b)

	workDir := b.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "hello.txt"), []byte("hello from mcp"), 0644); err != nil {
		b.Fatal(err)
	}

	b.Run("Cold_ProcessPerCall", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			stdioTransport := transport.NewStdio(serverBin, nil)
			c := client.NewClient(stdioTransport)
			if err := c.Start(ctx); err != nil {
				cancel()
				b.Fatalf("start failed: %v", err)
			}

			initReq := mcp.InitializeRequest{
				Params: mcp.InitializeParams{
					ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
					ClientInfo:      mcp.Implementation{Name: "bench", Version: "1.0"},
				},
			}
			if _, err := c.Initialize(ctx, initReq); err != nil {
				_ = c.Close()
				cancel()
				b.Fatalf("initialize failed: %v", err)
			}

			_, err := c.CallTool(ctx, mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name:      "read_file",
					Arguments: map[string]any{"path": "registry.go"},
				},
			})
			_ = c.Close()
			cancel()
			if err != nil {
				b.Fatalf("call failed: %v", err)
			}
		}
	})

	b.Run("Warm_PooledSession", func(b *testing.B) {
		reg, err := NewRegistry(workDir)
		if err != nil {
			b.Fatal(err)
		}
		defer reg.Close()

		callArgs := json.RawMessage(`{"command":"` + filepath.ToSlash(serverBin) + `","tool":"read_file","arguments":{"path":"registry.go"}}`)

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			approval, err := reg.Approve("mcp_call", callArgs)
			if err != nil {
				b.Fatalf("approve failed: %v", err)
			}
			out := reg.RunApproved(context.Background(), "mcp_call", callArgs, approval)
			if out == "" || out[:5] == "Error" {
				b.Fatalf("call failed: %s", out)
			}
		}
	})
}
