package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// REAMCPBinEnv specifies the absolute path to the local pre-installed REA binary.
	REAMCPBinEnv = "NABD_REA_MCP_BIN"
	// REAMCPSHA256Env specifies the required SHA-256 hex digest of the REA binary (mandatory for supply-chain pinning).
	REAMCPSHA256Env = "NABD_REA_MCP_SHA256"
	// REATargetDirEnv specifies the target directory for reverse engineering, mounted strictly Read-Only.
	REATargetDirEnv = "NABD_REA_TARGET_DIR"
	// REAMCPGuardEnv optionally overrides the path to the mcp-guard binary.
	REAMCPGuardEnv = "NABD_REA_MCP_GUARD"
	// REAMCPGuardSHA256Env optionally specifies the expected SHA-256 of the mcp-guard binary.
	REAMCPGuardSHA256Env = "NABD_REA_MCP_GUARD_SHA256"

	// REAServerName is the canonical MCP server name for REA.
	REAServerName = "rea"

	// maxREAMCPResponseBytes is the dedicated response limit for REA (32 KiB),
	// allowing decompilation outputs while preventing historyBudget exhaustion.
	maxREAMCPResponseBytes = 32 << 10
)

// REAConfig holds validated REA settings.
type REAConfig struct {
	BinPath   string
	BinSHA256 string
	TargetDir string
	Server    MCPServerConfig
}

// LoadREAConfigFromEnv reads and validates REA settings from the environment.
// It fails closed if any setting is missing, invalid, or violates safety invariants.
// If NABD_REA_MCP_BIN is empty, it returns nil, nil (REA is opt-in and disabled by default).
func LoadREAConfigFromEnv() (*REAConfig, error) {
	bin := strings.TrimSpace(os.Getenv(REAMCPBinEnv))
	if bin == "" {
		return nil, nil // Opt-in: disabled by default
	}

	// 1. Validate binary path (must be absolute)
	if !filepath.IsAbs(bin) {
		return nil, fmt.Errorf("مسار ثنائي REA (%s) غير صالح: يجب أن يكون مساراً مطلقاً (absolute path): %s", REAMCPBinEnv, bin)
	}

	// Supply-chain pinning: prohibit npx, npm, yarn or any dynamic package runners
	lowerBin := strings.ToLower(bin)
	if strings.Contains(lowerBin, "npx") || strings.Contains(lowerBin, "npm exec") || strings.Contains(lowerBin, "yarn dlx") {
		return nil, fmt.Errorf("حظر سلسلة التوريد: يُمنع منعاً باتاً تشغيل npx أو مديري الحزم الديناميكيين كخادم REA; ثبّت الأداة محلياً بنسخة ثابتة: %s", bin)
	}

	// Reject paths containing control characters or NUL
	if strings.ContainsAny(bin, "\x00\n\r\t") {
		return nil, fmt.Errorf("مسار ثنائي REA يحتوي على محارف تحكم غير مقبولة")
	}

	// 2. Validate mandatory SHA-256 digest (fail-closed)
	sha := strings.TrimSpace(os.Getenv(REAMCPSHA256Env))
	if sha == "" {
		return nil, fmt.Errorf("التحقق الأمني لـ REA إلزامي: متغير %s فارغ; يجب توفير تجزئة SHA-256 للملف التنفيذي لتثبيت سلسلة التوريد", REAMCPSHA256Env)
	}
	if !validDigest(sha) {
		return nil, fmt.Errorf("تجزئة SHA-256 غير صالحة في %s: يجب أن تكون بطول 64 حرفاً بنظام hex: %s", REAMCPSHA256Env, sha)
	}

	// Verify binary exists, is regular, and matches digest
	gotSHA, err := digestFile(bin)
	if err != nil {
		return nil, fmt.Errorf("فشل التحقق من ملف REA التنفيذي (%s): %w", bin, err)
	}
	if gotSHA != sha {
		return nil, fmt.Errorf("عدم تطابق تجزئة REA التنفيذي: المتوقع %s والمحسوب %s", sha, gotSHA)
	}

	// 3. Validate mandatory target directory (must be absolute, exist, and be a directory)
	targetDir := strings.TrimSpace(os.Getenv(REATargetDirEnv))
	if targetDir == "" {
		return nil, fmt.Errorf("مجلد هدف التحليل لـ REA إلزامي: يجب تعيين %s بمسار مطلق للمجلد المراد فحصه", REATargetDirEnv)
	}
	if !filepath.IsAbs(targetDir) {
		return nil, fmt.Errorf("مسار مجلد هدف REA (%s) غير صالح: يجب أن يكون مساراً مطلقاً: %s", REATargetDirEnv, targetDir)
	}
	info, err := os.Stat(targetDir)
	if err != nil {
		return nil, fmt.Errorf("تعذر الوصول إلى مجلد هدف REA (%s): %w", targetDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("مسار هدف REA ليس مجلداً: %s", targetDir)
	}

	// 4. Resolve and validate Guard
	guardPath := strings.TrimSpace(os.Getenv(REAMCPGuardEnv))
	guardSHA := strings.TrimSpace(os.Getenv(REAMCPGuardSHA256Env))
	if guardPath == "" {
		guardPath, err = mcpGuardPath()
		if err != nil {
			return nil, fmt.Errorf("تعذر تحديد مسار حارس العمليات mcp-guard: %w", err)
		}
	} else if !filepath.IsAbs(guardPath) {
		return nil, fmt.Errorf("مسار حارس REA (%s) يجب أن يكون مطلقاً: %s", REAMCPGuardEnv, guardPath)
	}

	gotGuardSHA, err := digestFile(guardPath)
	if err != nil {
		return nil, fmt.Errorf("فشل التحقق من حارس العمليات (%s): %w", guardPath, err)
	}
	if guardSHA != "" && gotGuardSHA != guardSHA {
		return nil, fmt.Errorf("عدم تطابق تجزئة حارس العمليات: المتوقع %s والمحسوب %s", guardSHA, gotGuardSHA)
	}
	guardSHA = gotGuardSHA

	// Build MCPServerConfig conforming to mcp_allowlist.go contract
	spec := MCPServerConfig{
		Executable:         bin,
		Args:               []string{"mcp"},
		SHA256:             sha,
		Guard:              guardPath,
		GuardSHA256:        guardSHA,
		ExtraReadonlyBinds: []string{targetDir},
		Env:                map[string]string{},
	}

	return &REAConfig{
		BinPath:   bin,
		BinSHA256: sha,
		TargetDir: targetDir,
		Server:    spec,
	}, nil
}

// PreflightREA performs early verification of REA environment variables.
// It fails closed and returns an actionable error if any setting is misconfigured.
func PreflightREA() error {
	_, err := LoadREAConfigFromEnv()
	return err
}
