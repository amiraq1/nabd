//go:build linux && (amd64 || arm64)

package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amiraq1/nabd/internal/sandbox"
)

// sandboxMCPSetup builds a static probe server and guard, writes a user
// allowlist, and returns a registry with isolation enabled. It never falls
// back to the unsandboxed execution path.
func sandboxMCPSetup(t *testing.T, mode string) (*Registry, json.RawMessage, string) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap is not installed; sandbox integration cannot be proven")
	}
	dir, server := f24Probe(t, mode)
	helpers := t.TempDir()
	limiter := buildStaticHelper(t, helpers, "nabd-limit")
	probe := buildStaticHelper(t, helpers, "nabd-sandbox-probe")
	guard := buildStaticHelper(t, helpers, "mcp-guard")
	sandbox.SetHelpersForTesting(limiter, probe)
	t.Cleanup(func() { sandbox.SetHelpersForTesting("", "") })

	serverSum, err := digestFile(server)
	if err != nil {
		t.Fatal(err)
	}
	guardSum, err := digestFile(guard)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := json.Marshal(mcpConfigFile{Servers: map[string]MCPServerConfig{
		"probe": {
			Executable:  server,
			SHA256:      serverSum,
			Guard:       guard,
			GuardSHA256: guardSum,
			Env:         map[string]string{"HOME": "/tmp", "PATH": "/usr/bin:/bin"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "allowlist.json")
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(MCPConfigEnv, cfgPath)
	t.Setenv("NABD_EXEC_SANDBOX", "1")
	r, err := NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.EnableExecution(context.Background()); err != nil {
		t.Fatalf("isolation gate did not open: %v", err)
	}
	args, _ := json.Marshal(map[string]any{"command": "probe", "tool": "probe"})
	return r, args, server
}

func TestSandboxMCPInitializeCallAndClose(t *testing.T) {
	r, args, _ := sandboxMCPSetup(t, "env")
	identity, err := r.MCPIdentity("probe")
	if err != nil {
		t.Fatal(err)
	}
	approval, err := r.ApproveWithIdentity("mcp_call", args, identity)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out := r.RunApproved(ctx, "mcp_call", args, approval)
	if strings.Contains(out, "Error:") {
		t.Fatalf("sandboxed MCP call failed: %s", out)
	}
	if !strings.Contains(out, "PATH") || !strings.Contains(out, "HOME") {
		t.Fatalf("sandboxed server did not return its allowlisted environment: %s", out)
	}
	if strings.Contains(out, "NABD_API_KEY") || strings.Contains(out, "F24_PRIVATE_TOKEN") {
		t.Fatalf("sandbox leaked parent environment: %s", out)
	}
}

func TestSandboxMCPRejectsUnconfiguredAndReboundServers(t *testing.T) {
	r, args, _ := sandboxMCPSetup(t, "env")
	identity, err := r.MCPIdentity("probe")
	if err != nil {
		t.Fatal(err)
	}

	other, _ := json.Marshal(map[string]any{"command": "not-configured", "tool": "probe"})
	if out := r.RunApproved(context.Background(), "mcp_call", other, Approval{}); !strings.Contains(out, "Error:") {
		t.Fatalf("unconfigured server was accepted: %s", out)
	}

	wrong := Approval{Token: "", Tool: "mcp_call", ArgsSHA256: hashBytes(args), IdentitySHA256: strings.Repeat("0", 64)}
	if out := r.RunApproved(context.Background(), "mcp_call", args, wrong); !strings.Contains(out, "invalid or expired") {
		t.Fatalf("different server identity was accepted: %s", out)
	}

	approval, err := r.ApproveWithIdentity("mcp_call", args, identity)
	if err != nil {
		t.Fatal(err)
	}
	approval.IdentitySHA256 = "changed"
	if out := r.RunApproved(context.Background(), "mcp_call", args, approval); !strings.Contains(out, "invalid or expired") {
		t.Fatalf("approval token with altered identity was accepted: %s", out)
	}
}

func TestSandboxMCPCancellationStopsServer(t *testing.T) {
	r, args, _ := sandboxMCPSetup(t, "hang")
	identity, err := r.MCPIdentity("probe")
	if err != nil {
		t.Fatal(err)
	}
	approval, err := r.ApproveWithIdentity("mcp_call", args, identity)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() { done <- r.RunApproved(ctx, "mcp_call", args, approval) }()
	cancel()
	select {
	case out := <-done:
		if !strings.Contains(out, "canceled") {
			t.Fatalf("cancellation was not reported: %s", out)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("sandboxed MCP call did not stop after cancellation")
	}
}

func TestSandboxMCPStaysDisabledWithoutOptIn(t *testing.T) {
	dir, server := f24Probe(t, "env")
	helpers := t.TempDir()
	guard := buildStaticHelper(t, helpers, "mcp-guard")
	serverSum, err := digestFile(server)
	if err != nil {
		t.Fatal(err)
	}
	guardSum, err := digestFile(guard)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := json.Marshal(mcpConfigFile{Servers: map[string]MCPServerConfig{
		"probe": {Executable: server, SHA256: serverSum, Guard: guard, GuardSHA256: guardSum},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "allowlist.json")
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(MCPConfigEnv, cfgPath)
	t.Setenv("NABD_EXEC_SANDBOX", "")
	r, err := NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.EnableExecution(context.Background()); err == nil {
		t.Fatal("execution enabled without explicit opt-in")
	}
	args, _ := json.Marshal(map[string]any{"command": "probe", "tool": "probe"})
	identity, err := r.MCPIdentity("probe")
	if err != nil {
		t.Fatal(err)
	}
	approval, err := r.ApproveWithIdentity("mcp_call", args, identity)
	if err != nil {
		t.Fatal(err)
	}
	if out := r.RunApproved(context.Background(), "mcp_call", args, approval); !strings.Contains(out, "disabled") {
		t.Fatalf("MCP ran without the opt-in proof: %s", out)
	}
}
