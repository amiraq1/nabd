package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amiraq1/nabd/internal/sandbox"
)

func writeAllowlist(t *testing.T, spec MCPServerConfig) string {
	t.Helper()
	data, err := json.Marshal(mcpConfigFile{Servers: map[string]MCPServerConfig{"probe": spec}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeExecutable(t *testing.T) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "exe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("#!/bin/sh\nexit 0\n"))
	return path, hex.EncodeToString(sum[:])
}

func TestMCPAllowlistRequiresExplicitConfig(t *testing.T) {
	if _, err := LoadMCPConfig(""); err == nil || !strings.Contains(err.Error(), MCPConfigEnv) {
		t.Fatalf("missing config must fail closed, got %v", err)
	}
}

func TestMCPAllowlistRejectsDigestMismatch(t *testing.T) {
	exe, _ := fakeExecutable(t)
	guard, guardSum := fakeExecutable(t)
	path := writeAllowlist(t, MCPServerConfig{
		Executable: exe, SHA256: strings.Repeat("0", 64),
		Guard: guard, GuardSHA256: guardSum,
	})
	if _, err := LoadMCPConfig(path); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("digest mismatch must be refused: %v", err)
	}
}

func TestMCPAllowlistRejectsUnlistedEnvironment(t *testing.T) {
	exe, exeSum := fakeExecutable(t)
	guard, guardSum := fakeExecutable(t)
	path := writeAllowlist(t, MCPServerConfig{
		Executable: exe, SHA256: exeSum,
		Guard: guard, GuardSHA256: guardSum,
		Env: map[string]string{"NABD_API_KEY": "secret"},
	})
	if _, err := LoadMCPConfig(path); err == nil || !strings.Contains(err.Error(), "not allowlisted") {
		t.Fatalf("secret environment must be refused: %v", err)
	}
}

func TestMCPAllowlistIdentityBindsServerAndGuard(t *testing.T) {
	exe, exeSum := fakeExecutable(t)
	guard, guardSum := fakeExecutable(t)
	base := MCPServerConfig{Executable: exe, SHA256: exeSum, Guard: guard, GuardSHA256: guardSum, Args: []string{"--stdio"}}
	cfg, err := LoadMCPConfig(writeAllowlist(t, base))
	if err != nil {
		t.Fatal(err)
	}
	id, err := cfg.Identity("probe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Resolve("probe", id); err != nil {
		t.Fatalf("valid identity was rejected: %v", err)
	}
	if _, err := cfg.Resolve("probe", "wrong"); err == nil {
		t.Fatal("mismatched approval identity was accepted")
	}
	if _, err := cfg.Resolve("other", id); err == nil {
		t.Fatal("unconfigured server was accepted")
	}
	changed := base
	changed.Args = []string{"--other"}
	cfg2, err := LoadMCPConfig(writeAllowlist(t, changed))
	if err != nil {
		t.Fatal(err)
	}
	id2, err := cfg2.Identity("probe")
	if err != nil {
		t.Fatal(err)
	}
	if id2 == id {
		t.Fatal("identity did not change with server arguments")
	}
}

func TestMCPAllowlistDetectsChangedFileAfterLoad(t *testing.T) {
	exe, exeSum := fakeExecutable(t)
	guard, guardSum := fakeExecutable(t)
	cfg, err := LoadMCPConfig(writeAllowlist(t, MCPServerConfig{
		Executable: exe, SHA256: exeSum, Guard: guard, GuardSHA256: guardSum,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Resolve("probe", ""); err == nil {
		t.Fatal("replaced executable was accepted")
	}
}

func TestMCPEnvironmentIsAllowlistOnly(t *testing.T) {
	t.Setenv("NABD_API_KEY", "synthetic")
	env := MCPEnvironment(MCPServerConfig{Env: map[string]string{"HOME": "/tmp/home", "TERM": "dumb"}})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "NABD_API_KEY") || len(env) != 2 {
		t.Fatalf("unexpected child environment: %q", env)
	}
}

func TestEnableExecutionRecordsMemoryLimitInAuditLog(t *testing.T) {
	exe, exeSum := fakeExecutable(t)
	guard, guardSum := fakeExecutable(t)
	cfgPath := writeAllowlist(t, MCPServerConfig{
		Executable: exe, SHA256: exeSum, Guard: guard, GuardSHA256: guardSum,
	})
	dir := t.TempDir()
	t.Setenv(MCPConfigEnv, cfgPath)
	t.Setenv("NABD_EXEC_SANDBOX", "1")
	t.Setenv(sandbox.MemoryLimitEnv, "2147483648")

	r, err := NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	err = r.EnableExecution(context.Background())
	if err != nil {
		if !errors.Is(err, sandbox.ErrUnavailable) {
			t.Fatalf("unexpected error from EnableExecution: %v", err)
		}
		return
	}

	events, _ := readAuditLog(t, dir)
	found := false
	for _, ev := range events {
		if ev.Event == "configured" && ev.Tool == "sandbox" && strings.Contains(ev.Detail, "memory_limit=") {
			found = true
			if !strings.Contains(ev.Detail, "2147483648") {
				t.Errorf("expected memory_limit=2147483648 in audit detail, got %s", ev.Detail)
			}
			break
		}
	}
	if !found {
		t.Fatalf("configured sandbox memory limit was not found in audit log: %+v", events)
	}
}

func TestLoadMCPConfigSecurityPolicies(t *testing.T) {
	exe, exeSum := fakeExecutable(t)
	guard, guardSum := fakeExecutable(t)
	spec := MCPServerConfig{
		Executable:  exe,
		SHA256:      exeSum,
		Guard:       guard,
		GuardSHA256: guardSum,
	}

	// 1. Backward compatibility: 0644 (group/other read-only, perm&0o022 == 0) MUST succeed.
	cfgPath := writeAllowlist(t, spec)
	if os.PathSeparator != '\\' {
		if err := os.Chmod(cfgPath, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadMCPConfig(cfgPath); err != nil {
			t.Fatalf("expected 0644 NABD_MCP_CONFIG to be accepted, got: %v", err)
		}

		// 2. Reject group-writable 0664 and world-writable 0666.
		for _, badMode := range []os.FileMode{0o664, 0o666, 0o620} {
			if err := os.Chmod(cfgPath, badMode); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadMCPConfig(cfgPath); err == nil || !strings.Contains(err.Error(), "group/other write") {
				t.Fatalf("expected group/other write rejection for mode %04o, got: %v", badMode, err)
			}
		}
		if err := os.Chmod(cfgPath, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// 3. Reject parent component symlink and leaf symlink in NABD_MCP_CONFIG.
	realDir := t.TempDir()
	realCfg := filepath.Join(realDir, "mcp.json")
	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realCfg, cfgBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	symParent := filepath.Join(t.TempDir(), "sym_cfg_dir")
	if err := os.Symlink(realDir, symParent); err == nil {
		if _, err := LoadMCPConfig(filepath.Join(symParent, "mcp.json")); err == nil || !strings.Contains(err.Error(), "symbolic links") {
			t.Fatalf("expected component symlink rejection, got: %v", err)
		}
	}
	leafLink := filepath.Join(t.TempDir(), "leaf.json")
	if err := os.Symlink(realCfg, leafLink); err == nil {
		if _, err := LoadMCPConfig(leafLink); err == nil || !strings.Contains(err.Error(), "symbolic links") {
			t.Fatalf("expected leaf symlink rejection, got: %v", err)
		}
	}

	// 4. Reject oversized allowlist > 1 MiB.
	bigPath := filepath.Join(t.TempDir(), "big.json")
	if err := os.WriteFile(bigPath, make([]byte, (1<<20)+10), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMCPConfig(bigPath); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected size limit error, got: %v", err)
	}

	// 5. Reject symlinked executable in server digest check.
	symExe := filepath.Join(t.TempDir(), "sym_exe")
	if err := os.Symlink(exe, symExe); err == nil {
		symCfg := writeAllowlist(t, MCPServerConfig{
			Executable:  symExe,
			SHA256:      exeSum,
			Guard:       guard,
			GuardSHA256: guardSum,
		})
		if _, err := LoadMCPConfig(symCfg); err == nil || !strings.Contains(err.Error(), "executable digest mismatch") {
			t.Fatalf("expected symlinked executable rejection, got: %v", err)
		}
	}
}
