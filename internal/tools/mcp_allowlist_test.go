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
