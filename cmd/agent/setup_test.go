package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		old, set := os.LookupEnv(k)
		if set {
			t.Cleanup(func() {
				_ = os.Setenv(k, old)
			})
			_ = os.Unsetenv(k)
		} else {
			t.Cleanup(func() {
				_ = os.Unsetenv(k)
			})
		}
	}
}

func setEnv(t *testing.T, key, val string) {
	t.Helper()
	old, set := os.LookupEnv(key)
	if set {
		t.Cleanup(func() {
			_ = os.Setenv(key, old)
		})
	} else {
		t.Cleanup(func() {
			_ = os.Unsetenv(key)
		})
	}
	if err := os.Setenv(key, val); err != nil {
		t.Fatalf("setenv %s: %v", key, err)
	}
}

func TestSetupSaveAndLoadConfigAndKey(t *testing.T) {
	clearEnv(t, "NABD_PROVIDER", "NABD_BASE_URL", "NABD_MODEL", "NABD_API_KEY", "NABD_API_KEY_FILE", "NABD_GEMINI_API_KEY", "NABD_GEMINI_API_KEY_FILE")
	dir := t.TempDir()

	cfg := SavedConfig{
		Provider: "openai",
		BaseURL:  "https://custom-openai.example.com/v1",
		Model:    "gpt-4o-mini",
	}
	secretKey := "sk-test-super-secret-key-12345"

	if err := SaveConfigAndKey(dir, cfg, secretKey); err != nil {
		t.Fatalf("SaveConfigAndKey failed: %v", err)
	}

	// 1. Verify config.json does NOT contain the secret key in any form
	configData, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("failed to read config.json: %v", err)
	}
	if strings.Contains(string(configData), secretKey) {
		t.Fatalf("SECURITY VIOLATION: secret key leaked into config.json: %s", string(configData))
	}
	if strings.Contains(string(configData), "api_key") || strings.Contains(string(configData), "gemini_api_key") {
		t.Fatalf("config.json contains unexpected key fields: %s", string(configData))
	}

	// 2. Verify key file exists with correct content
	keyPath := filepath.Join(dir, "api_key")
	savedKey, err := readSavedKey(keyPath)
	if err != nil {
		t.Fatalf("readSavedKey failed: %v", err)
	}
	if savedKey != secretKey {
		t.Fatalf("expected saved key %q, got %q", secretKey, savedKey)
	}

	// 3. Verify permissions on Unix
	if runtime.GOOS != "windows" {
		info, err := os.Stat(keyPath)
		if err != nil {
			t.Fatalf("os.Stat(keyPath): %v", err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("expected 0600 permissions, got %04o", info.Mode().Perm())
		}
	}

	// 4. LoadEffectiveConfig
	effCfg, keySource, err := LoadEffectiveConfig(dir)
	if err != nil {
		t.Fatalf("LoadEffectiveConfig failed: %v", err)
	}
	if keySource != "saved" {
		t.Fatalf("expected keySource 'saved', got %q", keySource)
	}
	if effCfg.Provider != "openai" {
		t.Fatalf("expected provider 'openai', got %q", effCfg.Provider)
	}
	if effCfg.BaseURL != cfg.BaseURL {
		t.Fatalf("expected BaseURL %q, got %q", cfg.BaseURL, effCfg.BaseURL)
	}
	if effCfg.Model != cfg.Model {
		t.Fatalf("expected Model %q, got %q", cfg.Model, effCfg.Model)
	}
	if effCfg.APIKey.SecretValue() != secretKey {
		t.Fatalf("expected exposed APIKey %q, got %q", secretKey, effCfg.APIKey.SecretValue())
	}
	if effCfg.APIKey.String() != "[REDACTED]" {
		t.Fatalf("expected string representation '[REDACTED]', got %q", effCfg.APIKey.String())
	}
}

func TestSetupProviderKeyIsolation(t *testing.T) {
	clearEnv(t, "NABD_PROVIDER", "NABD_BASE_URL", "NABD_MODEL", "NABD_API_KEY", "NABD_API_KEY_FILE", "NABD_GEMINI_API_KEY", "NABD_GEMINI_API_KEY_FILE")
	dir := t.TempDir()

	openAIKey := "sk-openai-key-999"
	geminiKey := "AIzaSy-gemini-key-888"

	// Save OpenAI config and key
	cfgOpenAI := SavedConfig{
		Provider: "openai",
		BaseURL:  "https://api.openai.com/v1",
		Model:    "gpt-4o",
	}
	if err := SaveConfigAndKey(dir, cfgOpenAI, openAIKey); err != nil {
		t.Fatalf("save openai: %v", err)
	}

	// If NABD_PROVIDER is switched to gemini, OpenAI saved key must NOT be used!
	setEnv(t, "NABD_PROVIDER", "gemini")
	effCfg, keySource, err := LoadEffectiveConfig(dir)
	if err != nil {
		t.Fatalf("LoadEffectiveConfig: %v", err)
	}
	if keySource != "none" {
		t.Fatalf("expected keySource 'none' when gemini key is missing, got %q", keySource)
	}
	if effCfg.APIKey.SecretValue() != "" {
		t.Fatalf("CRITICAL SECURITY ERROR: OpenAI key was used for Gemini provider: %q", effCfg.APIKey.SecretValue())
	}

	// Now write Gemini key
	cfgGemini := SavedConfig{
		Provider: "gemini",
		BaseURL:  "https://generativelanguage.googleapis.com/v1beta",
		Model:    "gemini-2.5-flash",
	}
	if err := SaveConfigAndKey(dir, cfgGemini, geminiKey); err != nil {
		t.Fatalf("save gemini: %v", err)
	}

	// Effective config for Gemini should now load gemini_api_key
	effCfg, keySource, err = LoadEffectiveConfig(dir)
	if err != nil {
		t.Fatalf("LoadEffectiveConfig: %v", err)
	}
	if keySource != "saved" {
		t.Fatalf("expected keySource 'saved', got %q", keySource)
	}
	if effCfg.APIKey.SecretValue() != geminiKey {
		t.Fatalf("expected Gemini key %q, got %q", geminiKey, effCfg.APIKey.SecretValue())
	}

	// Switch back to OpenAI: effective config must load OpenAI key and NOT Gemini key
	setEnv(t, "NABD_PROVIDER", "openai")
	effCfg, keySource, err = LoadEffectiveConfig(dir)
	if err != nil {
		t.Fatalf("LoadEffectiveConfig: %v", err)
	}
	if keySource != "saved" {
		t.Fatalf("expected keySource 'saved', got %q", keySource)
	}
	if effCfg.APIKey.SecretValue() != openAIKey {
		t.Fatalf("expected OpenAI key %q, got %q", openAIKey, effCfg.APIKey.SecretValue())
	}
}

func TestSetupEnvPrecedenceOverSaved(t *testing.T) {
	clearEnv(t, "NABD_PROVIDER", "NABD_BASE_URL", "NABD_MODEL", "NABD_API_KEY", "NABD_API_KEY_FILE", "NABD_GEMINI_API_KEY", "NABD_GEMINI_API_KEY_FILE")
	dir := t.TempDir()

	savedKey := "sk-saved-key"
	cfg := SavedConfig{
		Provider: "openai",
		BaseURL:  "https://saved.example.com/v1",
		Model:    "saved-model",
	}
	if err := SaveConfigAndKey(dir, cfg, savedKey); err != nil {
		t.Fatalf("SaveConfigAndKey: %v", err)
	}

	// 1. Env overrides model, baseURL, and API key
	setEnv(t, "NABD_MODEL", "env-model-override")
	setEnv(t, "NABD_BASE_URL", "https://env.example.com/v1")
	setEnv(t, "NABD_API_KEY", "sk-env-key-override")

	effCfg, keySource, err := LoadEffectiveConfig(dir)
	if err != nil {
		t.Fatalf("LoadEffectiveConfig: %v", err)
	}
	if keySource != "env" {
		t.Fatalf("expected keySource 'env', got %q", keySource)
	}
	if effCfg.Model != "env-model-override" {
		t.Fatalf("expected model 'env-model-override', got %q", effCfg.Model)
	}
	if effCfg.BaseURL != "https://env.example.com/v1" {
		t.Fatalf("expected baseURL 'https://env.example.com/v1', got %q", effCfg.BaseURL)
	}
	if effCfg.APIKey.SecretValue() != "sk-env-key-override" {
		t.Fatalf("expected APIKey 'sk-env-key-override', got %q", effCfg.APIKey.SecretValue())
	}

	// 2. NABD_API_KEY_FILE takes precedence over saved key when NABD_API_KEY is unset
	clearEnv(t, "NABD_API_KEY")
	keyFileDir := t.TempDir()
	keyFilePath := filepath.Join(keyFileDir, "test_file_key")
	if err := os.WriteFile(keyFilePath, []byte("sk-file-key-content\n"), 0600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	setEnv(t, "NABD_API_KEY_FILE", keyFilePath)

	effCfg, keySource, err = LoadEffectiveConfig(dir)
	if err != nil {
		t.Fatalf("LoadEffectiveConfig: %v", err)
	}
	if keySource != "env_file" {
		t.Fatalf("expected keySource 'env_file', got %q", keySource)
	}
	if effCfg.APIKey.SecretValue() != "sk-file-key-content" {
		t.Fatalf("expected APIKey 'sk-file-key-content', got %q", effCfg.APIKey.SecretValue())
	}
}

func TestSetupBothEnvKeyAndFileFails(t *testing.T) {
	clearEnv(t, "NABD_PROVIDER", "NABD_API_KEY", "NABD_API_KEY_FILE")
	dir := t.TempDir()

	setEnv(t, "NABD_API_KEY", "key1")
	setEnv(t, "NABD_API_KEY_FILE", "file1")

	_, _, err := LoadEffectiveConfig(dir)
	if err == nil {
		t.Fatal("expected error when both NABD_API_KEY and NABD_API_KEY_FILE are set, got nil")
	}
	if !strings.Contains(err.Error(), "both NABD_API_KEY and NABD_API_KEY_FILE are set") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestSetupReadSavedKeySecurity(t *testing.T) {
	dir := t.TempDir()

	// 1. Valid file
	validPath := filepath.Join(dir, "valid_key")
	if err := os.WriteFile(validPath, []byte("valid-secret-token\n"), 0600); err != nil {
		t.Fatalf("write valid key: %v", err)
	}
	got, err := readSavedKey(validPath)
	if err != nil {
		t.Fatalf("readSavedKey valid: %v", err)
	}
	if got != "valid-secret-token" {
		t.Fatalf("expected 'valid-secret-token', got %q", got)
	}

	// 2. Reject file > 4096 bytes
	largePath := filepath.Join(dir, "large_key")
	largeData := bytes.Repeat([]byte("A"), 4097)
	if err := os.WriteFile(largePath, largeData, 0600); err != nil {
		t.Fatalf("write large key: %v", err)
	}
	_, err = readSavedKey(largePath)
	if err == nil || !strings.Contains(err.Error(), "exceeds 4096 bytes limit") {
		t.Fatalf("expected size limit error, got %v", err)
	}

	// 3. Reject symlink
	symlinkPath := filepath.Join(dir, "symlink_key")
	if err := os.Symlink(validPath, symlinkPath); err == nil {
		_, err = readSavedKey(symlinkPath)
		if err == nil || !strings.Contains(err.Error(), "symlinks are not allowed") {
			t.Fatalf("expected symlink rejection error, got %v", err)
		}
	}

	// 4. Reject loose permissions on Unix
	if runtime.GOOS != "windows" {
		loosePath := filepath.Join(dir, "loose_key")
		if err := os.WriteFile(loosePath, []byte("secret"), 0644); err != nil {
			t.Fatalf("write loose key: %v", err)
		}
		_, err = readSavedKey(loosePath)
		if err == nil || !strings.Contains(err.Error(), "permissions must be 0600 or stricter") {
			t.Fatalf("expected permission rejection error, got %v", err)
		}
	}
}

func TestSetupValidateConfigDirNotInWorkspace(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	// 1. Subdirectory inside workspace must be rejected
	inside := filepath.Join(workspace, "config")
	if err := validateConfigDirNotInWorkspace(inside, workspace); err == nil {
		t.Fatal("expected rejection for config dir inside workspace, got nil")
	}

	// 2. Workspace itself must be rejected
	if err := validateConfigDirNotInWorkspace(workspace, workspace); err == nil {
		t.Fatal("expected rejection for config dir equal to workspace, got nil")
	}

	// 3. Directory containing .git component must be rejected
	gitDir := filepath.Join(t.TempDir(), ".git", "nabd")
	if err := validateConfigDirNotInWorkspace(gitDir, workspace); err == nil {
		t.Fatal("expected rejection for config dir in .git, got nil")
	}

	// 4. Outside directory must be allowed
	outside := filepath.Join(t.TempDir(), "user_home", "nabd")
	if err := validateConfigDirNotInWorkspace(outside, workspace); err != nil {
		t.Fatalf("expected outside config dir to be accepted, got error: %v", err)
	}
}

func TestSetupReset(t *testing.T) {
	dir := t.TempDir()

	// Create config.json, api_key, gemini_api_key, and an unrelated file
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0600)
	_ = os.WriteFile(filepath.Join(dir, "api_key"), []byte("key1"), 0600)
	_ = os.WriteFile(filepath.Join(dir, "gemini_api_key"), []byte("key2"), 0600)
	unrelatedPath := filepath.Join(dir, "unrelated.txt")
	_ = os.WriteFile(unrelatedPath, []byte("keep-me"), 0644)

	if err := ResetConfig(dir); err != nil {
		t.Fatalf("ResetConfig: %v", err)
	}

	// Config and key files must be removed
	for _, f := range []string{"config.json", "api_key", "gemini_api_key"} {
		p := filepath.Join(dir, f)
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("file %s should have been deleted, but still exists", f)
		}
	}

	// Unrelated file must NOT be removed
	if _, err := os.Stat(unrelatedPath); err != nil {
		t.Fatalf("unrelated file should not have been deleted: %v", err)
	}
}

func TestSetupInteractiveRunWithOptions(t *testing.T) {
	// 1. --help option
	{
		var stdout, stderr bytes.Buffer
		code := runSetupWithOptions(setupOptions{
			Stdout: &stdout,
			Stderr: &stderr,
			Args:   []string{"--help"},
		})
		if code != 0 {
			t.Fatalf("expected exit code 0 for --help, got %d", code)
		}
		if !strings.Contains(stdout.String(), "Usage: nabd-agent setup") {
			t.Fatalf("expected usage in stdout, got %s", stdout.String())
		}
	}

	// 2. Non-interactive terminal check
	{
		var stdout, stderr bytes.Buffer
		code := runSetupWithOptions(setupOptions{
			Stdout:     &stdout,
			Stderr:     &stderr,
			IsTerminal: func() bool { return false },
		})
		if code != 1 {
			t.Fatalf("expected exit code 1 for non-interactive terminal, got %d", code)
		}
		if !strings.Contains(stderr.String(), "interactive terminal required") {
			t.Fatalf("expected interactive terminal error, got %s", stderr.String())
		}
	}

	// 3. --reset interactive confirmation "y"
	{
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0600)
		_ = os.WriteFile(filepath.Join(dir, "api_key"), []byte("secret"), 0600)

		var stdout, stderr bytes.Buffer
		code := runSetupWithOptions(setupOptions{
			Stdin:      strings.NewReader("y\n"),
			Stdout:     &stdout,
			Stderr:     &stderr,
			ConfigDir:  dir,
			Args:       []string{"--reset"},
			IsTerminal: func() bool { return true },
		})
		if code != 0 {
			t.Fatalf("expected exit code 0, got %d", code)
		}
		if !strings.Contains(stdout.String(), "successfully removed") {
			t.Fatalf("expected removal confirmation, got %s", stdout.String())
		}
		if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
			t.Fatalf("config.json should be removed")
		}
	}

	// 4. Full interactive setup with mock server and connection verification
	{
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Mock OpenAI /models endpoint
			auth := r.Header.Get("Authorization")
			if auth != "Bearer test-mock-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"mock-model"}]}`))
		}))
		defer ts.Close()

		dir := t.TempDir()
		workspace := t.TempDir()

		// Inputs for prompts:
		// 1. Provider: openai
		// 2. Base URL: ts.URL
		// 3. Model: mock-model
		// (API Key is provided via ReadSecret mock)
		// 4. Test connection: y
		userInput := strings.Join([]string{
			"openai",
			ts.URL,
			"mock-model",
			"y",
		}, "\n") + "\n"

		var stdout, stderr bytes.Buffer
		code := runSetupWithOptions(setupOptions{
			Stdin:        strings.NewReader(userInput),
			Stdout:       &stdout,
			Stderr:       &stderr,
			ConfigDir:    dir,
			WorkspaceDir: workspace,
			IsTerminal:   func() bool { return true },
			ReadSecret: func(prompt string) (string, error) {
				return "test-mock-key", nil
			},
		})

		if code != 0 {
			t.Fatalf("expected exit code 0, got %d (stderr: %s)", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "✓ Connection verified successfully!") {
			t.Fatalf("expected connection verified in stdout, got:\n%s", stdout.String())
		}
		if !strings.Contains(stdout.String(), "Configuration saved to:") {
			t.Fatalf("expected saved message in stdout, got:\n%s", stdout.String())
		}

		// Verify files
		savedCfg, err := loadSavedConfig(dir)
		if err != nil {
			t.Fatalf("loadSavedConfig: %v", err)
		}
		if savedCfg.Provider != "openai" || savedCfg.BaseURL != ts.URL || savedCfg.Model != "mock-model" {
			t.Fatalf("unexpected saved config: %+v", savedCfg)
		}

		key, err := readSavedKey(filepath.Join(dir, "api_key"))
		if err != nil {
			t.Fatalf("readSavedKey: %v", err)
		}
		if key != "test-mock-key" {
			t.Fatalf("expected saved key 'test-mock-key', got %q", key)
		}
	}
}

func TestStatusShowsDiagnosticsAndKeySource(t *testing.T) {
	m := &uiModel{
		provider:      "openai",
		modelName:     "gpt-4o",
		destination:   "https://api.openai.com/v1",
		maxIterations: 10,
	}
	m.setKeySource("saved")

	status := m.formatStatus()
	if !strings.Contains(status, "API Key: saved") {
		t.Fatalf("expected 'API Key: saved' in formatStatus(), got:\n%s", status)
	}
	if !strings.Contains(m.statusBar, "key=saved") {
		t.Fatalf("expected 'key=saved' in statusBar, got:\n%s", m.statusBar)
	}

	m.setKeySource("env")
	if !strings.Contains(m.formatStatus(), "API Key: env") {
		t.Fatalf("expected 'API Key: env' in formatStatus(), got:\n%s", m.formatStatus())
	}
	if !strings.Contains(m.statusBar, "key=env") {
		t.Fatalf("expected 'key=env' in statusBar, got:\n%s", m.statusBar)
	}
}
