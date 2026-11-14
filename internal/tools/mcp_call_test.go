package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mcpProbeScript(t *testing.T, body string) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatalf("sh not found: %v", err)
	}
	path := filepath.Join(t.TempDir(), "probe.sh")
	if err := os.WriteFile(path, []byte("#!"+sh+"\n"+body+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func mcpArgs(t *testing.T, command string) json.RawMessage {
	t.Helper()
	return json.RawMessage(fmt.Sprintf(`{"command":%q,"tool":"x"}`, command))
}

func TestMCPCallDoesNotInheritEnvironment(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "env.txt")
	script := mcpProbeScript(t, `for v in $(env); do echo "${v%%=*}"; done > `+outFile)
	t.Setenv("NABD_MCP_SENTINEL", "sentinel")
	t.Setenv("NABD_API_KEY", "sentinel")

	r := mustRegistry(t, dir)
	out := r.RunApproved(context.Background(), "mcp_call", mcpArgs(t, script), ApprovalState{})
	_ = out

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("probe did not run: %v", err)
	}
	names := string(data)
	if strings.Contains(names, "NABD_MCP_SENTINEL") {
		t.Fatal("child inherited NABD_MCP_SENTINEL")
	}
	if strings.Contains(names, "NABD_API_KEY") {
		t.Fatal("child inherited NABD_API_KEY")
	}
}

func TestMCPCallTimeoutKillsProcessTree(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "alive.txt")
	script := mcpProbeScript(t, `( sleep 2; printf alive > `+marker+` ) &`+"\nwait")

	r := mustRegistry(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	out := r.RunApproved(ctx, "mcp_call", mcpArgs(t, script), ApprovalState{})
	if !strings.Contains(out, "Error") {
		t.Fatalf("expected an error, got %q", out)
	}

	time.Sleep(3 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("grandchild survived the mcp_call timeout")
	}
}

func TestMCPCallClientErrorKillsProcessTree(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "alive.txt")
	script := mcpProbeScript(t, `( sleep 2; printf alive > `+marker+` ) &`+"\nexit 1")

	r := mustRegistry(t, dir)
	out := r.RunApproved(context.Background(), "mcp_call", mcpArgs(t, script), ApprovalState{})
	if !strings.Contains(out, "Error") {
		t.Fatalf("expected an error, got %q", out)
	}

	time.Sleep(3 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("grandchild survived the mcp_call client error")
	}
}

func TestMCPCallDoesNotUseShell(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "shell.txt")
	r := mustRegistry(t, dir)
	command := "true; touch " + marker

	out := r.RunApproved(context.Background(), "mcp_call", mcpArgs(t, command), ApprovalState{})
	if !strings.Contains(out, "Error") {
		t.Fatalf("expected a start error, got %q", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("shell metacharacter was executed")
	}
}

func TestMCPCallRunRequiresApproval(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran.txt")
	script := mcpProbeScript(t, "touch "+marker)
	r := mustRegistry(t, dir)

	out := r.Run(context.Background(), "mcp_call", mcpArgs(t, script))
	if !strings.Contains(out, "requires approval") {
		t.Fatalf("Run did not fail closed: %q", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("Run executed mcp_call without approval")
	}
}
