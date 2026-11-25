package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestREAConfigDisabledByDefault(t *testing.T) {
	t.Setenv(REAMCPBinEnv, "")
	t.Setenv(REAMCPSHA256Env, "")
	t.Setenv(REATargetDirEnv, "")

	cfg, err := LoadREAConfigFromEnv()
	if err != nil {
		t.Fatalf("expected nil error when disabled, got: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config when disabled, got: %+v", cfg)
	}
	if err := PreflightREA(); err != nil {
		t.Fatalf("expected nil preflight error when disabled, got: %v", err)
	}
}

func TestREAConfigRejectsNPXAndRelativePaths(t *testing.T) {
	// 1. Relative path rejected
	t.Setenv(REAMCPBinEnv, "rea")
	_, err := LoadREAConfigFromEnv()
	if err == nil || !filepath.IsAbs("rea") && err == nil {
		t.Fatal("expected error for relative binary path")
	}

	// 2. npx rejected
	t.Setenv(REAMCPBinEnv, "/usr/local/bin/npx")
	t.Setenv(REAMCPSHA256Env, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	t.Setenv(REATargetDirEnv, t.TempDir())
	_, err = LoadREAConfigFromEnv()
	if err == nil {
		t.Fatal("expected error prohibiting npx")
	}
}

func TestREAConfigRequiresMandatorySHA256(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "rea-mock")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	targetDir := t.TempDir()

	t.Setenv(REAMCPBinEnv, binPath)
	t.Setenv(REATargetDirEnv, targetDir)

	// Missing SHA256
	t.Setenv(REAMCPSHA256Env, "")
	_, err := LoadREAConfigFromEnv()
	if err == nil {
		t.Fatal("expected error when NABD_REA_MCP_SHA256 is missing")
	}

	// Invalid hex length
	t.Setenv(REAMCPSHA256Env, "not-a-valid-sha256")
	_, err = LoadREAConfigFromEnv()
	if err == nil {
		t.Fatal("expected error for invalid hex length in NABD_REA_MCP_SHA256")
	}

	// Digest mismatch
	t.Setenv(REAMCPSHA256Env, "0000000000000000000000000000000000000000000000000000000000000000")
	_, err = LoadREAConfigFromEnv()
	if err == nil {
		t.Fatal("expected digest mismatch error")
	}
}

func TestREAConfigRequiresTargetDir(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "rea-mock")
	content := []byte("#!/bin/sh\necho ok\n")
	if err := os.WriteFile(binPath, content, 0o755); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(content)
	hexHash := hex.EncodeToString(hash[:])

	t.Setenv(REAMCPBinEnv, binPath)
	t.Setenv(REAMCPSHA256Env, hexHash)

	// Missing target dir
	t.Setenv(REATargetDirEnv, "")
	_, err := LoadREAConfigFromEnv()
	if err == nil {
		t.Fatal("expected error when NABD_REA_TARGET_DIR is missing")
	}

	// Relative target dir
	t.Setenv(REATargetDirEnv, "relative/dir")
	_, err = LoadREAConfigFromEnv()
	if err == nil {
		t.Fatal("expected error when NABD_REA_TARGET_DIR is relative")
	}

	// Non-existent target dir
	t.Setenv(REATargetDirEnv, filepath.Join(dir, "does-not-exist"))
	_, err = LoadREAConfigFromEnv()
	if err == nil {
		t.Fatal("expected error when NABD_REA_TARGET_DIR does not exist")
	}
}

func TestREAConfigValidSetup(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "rea-mock")
	binContent := []byte("#!/bin/sh\necho ok\n")
	if err := os.WriteFile(binPath, binContent, 0o755); err != nil {
		t.Fatal(err)
	}
	binHash := sha256.Sum256(binContent)
	hexBinHash := hex.EncodeToString(binHash[:])

	guardPath := filepath.Join(dir, "mock-guard")
	guardContent := []byte("#!/bin/sh\nexec \"$@\"\n")
	if err := os.WriteFile(guardPath, guardContent, 0o755); err != nil {
		t.Fatal(err)
	}
	guardHash := sha256.Sum256(guardContent)
	hexGuardHash := hex.EncodeToString(guardHash[:])

	targetDir := filepath.Join(dir, "target")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv(REAMCPBinEnv, binPath)
	t.Setenv(REAMCPSHA256Env, hexBinHash)
	t.Setenv(REATargetDirEnv, targetDir)
	t.Setenv(REAMCPGuardEnv, guardPath)
	t.Setenv(REAMCPGuardSHA256Env, hexGuardHash)

	cfg, err := LoadREAConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error for valid config: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.BinPath != binPath || cfg.BinSHA256 != hexBinHash || cfg.TargetDir != targetDir {
		t.Fatalf("config values mismatch: %+v", cfg)
	}
	if len(cfg.Server.ExtraReadonlyBinds) != 1 || cfg.Server.ExtraReadonlyBinds[0] != targetDir {
		t.Fatalf("expected ExtraReadonlyBinds to contain targetDir: %v", cfg.Server.ExtraReadonlyBinds)
	}
}
