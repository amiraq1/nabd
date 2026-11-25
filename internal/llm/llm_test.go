package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigFromEnvProvider(t *testing.T) {
	// Test default provider (openai)
	t.Setenv("NABD_PROVIDER", "")
	t.Setenv("NABD_BASE_URL", "")
	t.Setenv("NABD_MODEL", "")
	t.Setenv("NABD_API_KEY", "key-openai")
	t.Setenv("NABD_GEMINI_API_KEY", "")
	t.Setenv("NABD_GEMINI_SEARCH", "")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Provider != ProviderOpenAI {
		t.Fatalf("expected provider %q, got %q", ProviderOpenAI, cfg.Provider)
	}
	if cfg.BaseURL != DefaultBaseURL {
		t.Fatalf("expected default baseURL %q, got %q", DefaultBaseURL, cfg.BaseURL)
	}
	if cfg.Model != DefaultModel {
		t.Fatalf("expected default model %q, got %q", DefaultModel, cfg.Model)
	}

	// Test gemini provider defaults and case-insensitivity
	t.Setenv("NABD_PROVIDER", " GeMiNi ")
	cfg, err = ConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Provider != "gemini" {
		t.Fatalf("expected provider %q, got %q", "gemini", cfg.Provider)
	}
	if cfg.BaseURL != DefaultGeminiBaseURL {
		t.Fatalf("expected default gemini baseURL %q, got %q", DefaultGeminiBaseURL, cfg.BaseURL)
	}
	if cfg.Model != DefaultGeminiModel {
		t.Fatalf("expected default gemini model %q, got %q", DefaultGeminiModel, cfg.Model)
	}
	// Fallback to NABD_API_KEY when NABD_GEMINI_API_KEY is unset
	if cfg.APIKey.SecretValue() != "key-openai" {
		t.Fatalf("expected fallback key 'key-openai', got %q", cfg.APIKey.SecretValue())
	}

	// Test dedicated NABD_GEMINI_API_KEY overrides fallback
	t.Setenv("NABD_GEMINI_API_KEY", "key-gemini-dedicated")
	cfg, err = ConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.APIKey.SecretValue() != "key-gemini-dedicated" {
		t.Fatalf("expected dedicated key 'key-gemini-dedicated', got %q", cfg.APIKey.SecretValue())
	}

	// Test NABD_GEMINI_SEARCH
	t.Setenv("NABD_GEMINI_SEARCH", "1")
	cfg, err = ConfigFromEnv()
	if err != nil || !cfg.GeminiSearch {
		t.Fatalf("expected GeminiSearch=true, got err=%v, val=%v", err, cfg.GeminiSearch)
	}

	t.Setenv("NABD_GEMINI_SEARCH", "0")
	cfg, err = ConfigFromEnv()
	if err != nil || cfg.GeminiSearch {
		t.Fatalf("expected GeminiSearch=false, got err=%v, val=%v", err, cfg.GeminiSearch)
	}

	t.Setenv("NABD_GEMINI_SEARCH", "invalid_val")
	_, err = ConfigFromEnv()
	if err == nil || !strings.Contains(err.Error(), "NABD_GEMINI_SEARCH") {
		t.Fatalf("expected error for invalid NABD_GEMINI_SEARCH, got %v", err)
	}
}

func TestGeminiKeyConflictAndFile(t *testing.T) {
	tempDir := t.TempDir()
	keyFile := filepath.Join(tempDir, "gemini_key.txt")
	if err := os.WriteFile(keyFile, []byte("gemini-secret-from-file\n"), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("NABD_PROVIDER", "gemini")
	t.Setenv("NABD_GEMINI_API_KEY", "some-key")
	t.Setenv("NABD_GEMINI_API_KEY_FILE", keyFile)

	// Both set -> error
	_, err := ConfigFromEnv()
	if err == nil || !strings.Contains(err.Error(), "both NABD_GEMINI_API_KEY and NABD_GEMINI_API_KEY_FILE are set") {
		t.Fatalf("expected conflict error, got %v", err)
	}

	// File only -> success
	t.Setenv("NABD_GEMINI_API_KEY", "")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error reading key file: %v", err)
	}
	if cfg.APIKey.SecretValue() != "gemini-secret-from-file" {
		t.Fatalf("expected 'gemini-secret-from-file', got %q", cfg.APIKey.SecretValue())
	}
}

func TestConfigValidation(t *testing.T) {
	// Unknown provider
	cfg := Config{
		Provider: "anthropic",
		BaseURL:  "https://api.example.com",
		Model:    "claude",
		APIKey:   "secret-key-12345",
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "invalid NABD_PROVIDER") {
		t.Fatalf("expected invalid NABD_PROVIDER error, got %v", err)
	}

	// Gemini loopback allowed without key
	cfg = Config{
		Provider: "gemini",
		BaseURL:  "http://127.0.0.1:8080",
		Model:    "gemini-2.5-flash",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("loopback should not require API key, got %v", err)
	}

	// Remote requires key
	cfg.BaseURL = "https://generativelanguage.googleapis.com/v1beta"
	if err := cfg.Validate(); err == nil {
		t.Fatal("remote Gemini endpoint must require API key")
	}

	// Insecure remote HTTP rejected
	cfg.BaseURL = "http://generativelanguage.googleapis.com/v1beta"
	cfg.APIKey = "secret-key-12345"
	if err := cfg.Validate(); err == nil {
		t.Fatal("insecure remote HTTP must be rejected")
	}
}

func TestNewClientFactory(t *testing.T) {
	// OpenAI dispatch
	cfgOpenAI := Config{
		Provider: "openai",
		BaseURL:  "http://127.0.0.1:8080/v1",
		Model:    "local",
	}
	client, err := NewClient(cfgOpenAI)
	if err != nil {
		t.Fatalf("NewClient openai failed: %v", err)
	}
	if _, ok := client.(*OpenAIClient); !ok {
		t.Fatalf("expected *OpenAIClient, got %T", client)
	}

	// Gemini dispatch
	cfgGemini := Config{
		Provider: "gemini",
		BaseURL:  "http://127.0.0.1:8080",
		Model:    "gemini-2.5-flash",
	}
	client, err = NewClient(cfgGemini)
	if err != nil {
		t.Fatalf("NewClient gemini failed: %v", err)
	}
	gemClient, ok := client.(*GeminiClient)
	if !ok {
		t.Fatalf("expected *GeminiClient, got %T", client)
	}
	if gemClient.Provider() != "gemini" {
		t.Fatalf("expected Provider() 'gemini', got %q", gemClient.Provider())
	}
}
