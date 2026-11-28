package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("NABD_BASE_URL", "https://example.invalid/v1")
	t.Setenv("NABD_MODEL", "test-model")
	t.Setenv("NABD_API_KEY", "test-key")
	t.Setenv("NABD_API_KEY_FILE", "")
	t.Setenv("NABD_TIMEOUT", "")
	t.Setenv("NABD_MAX_TOKENS", "")
	t.Setenv("NABD_TEMPERATURE", "")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv = %v", err)
	}
	if cfg.BaseURL != "https://example.invalid/v1" || cfg.Model != "test-model" || cfg.APIKey.SecretValue() != "test-key" {
		t.Fatalf("ConfigFromEnv = %+v", cfg)
	}

	t.Setenv("NABD_BASE_URL", "")
	t.Setenv("NABD_MODEL", "")
	t.Setenv("NABD_API_KEY", "")
	t.Setenv("NABD_API_KEY_FILE", "")
	cfg, err = ConfigFromEnv()
	if err != nil {
		t.Fatalf("defaults error = %v", err)
	}
	if cfg.BaseURL != DefaultBaseURL || cfg.Model != DefaultModel || cfg.APIKey != "" {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestNewOpenAIClientDefaults(t *testing.T) {
	client := NewOpenAIClient(Config{})
	if client.cfg.BaseURL != DefaultBaseURL || client.cfg.Model != DefaultModel || client.cfg.APIKey != "" {
		t.Fatalf("defaults = %+v", client.cfg)
	}
	if client.cfg.MaxTokens != DefaultMaxTokens || client.cfg.GetTemperature() != DefaultTemperature {
		t.Fatalf("generation defaults = %+v", client.cfg)
	}
}

func TestChatSuccess(t *testing.T) {
	var seen struct {
		Method string
		Auth   string
		Path   string
		Body   struct {
			Model       string           `json:"model"`
			Messages    []Message        `json:"messages"`
			Tools       []map[string]any `json:"tools"`
			Temperature float64          `json:"temperature"`
			MaxTokens   int              `json:"max_tokens"`
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Method = r.Method
		seen.Auth = r.Header.Get("Authorization")
		seen.Path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&seen.Body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"content": "hi",
					"tool_calls": [
						{"function": {"name": "read_file", "arguments": "{\"path\":\"a\"}"}},
						{"function": {"name": "list_dir", "arguments": "{}"}}
					]
				},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3}
		}`))
	}))
	defer server.Close()

	client := NewOpenAIClient(Config{
		BaseURL: server.URL + "/v1",
		Model:   "test-model",
		APIKey:  "test-key",
		Timeout: 5 * time.Second,
	})
	resp, err := client.Chat(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "hello"}},
		Tools:    []map[string]any{{"type": "function"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if seen.Method != http.MethodPost || seen.Path != "/v1/chat/completions" {
		t.Fatalf("request = %s %s", seen.Method, seen.Path)
	}
	if seen.Auth != "Bearer test-key" {
		t.Fatalf("auth = %q", seen.Auth)
	}
	if seen.Body.Model != "test-model" || seen.Body.MaxTokens != DefaultMaxTokens || seen.Body.Temperature != DefaultTemperature {
		t.Fatalf("payload = %+v", seen.Body)
	}
	if len(seen.Body.Messages) != 1 || len(seen.Body.Tools) != 1 {
		t.Fatalf("payload messages/tools = %+v", seen.Body)
	}

	if resp.Message.Content != "hi" || resp.Message.Role != "assistant" {
		t.Fatalf("message = %+v", resp.Message)
	}
	if len(resp.Message.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	ids := map[string]bool{}
	for _, tc := range resp.Message.ToolCalls {
		if tc.ID == "" || tc.Type != "function" {
			t.Fatalf("tool call not normalized: %+v", tc)
		}
		if ids[tc.ID] {
			t.Fatalf("duplicate generated call id %q", tc.ID)
		}
		ids[tc.ID] = true
	}
	if resp.FinishReason != "stop" || resp.Usage.TotalTokens != 3 {
		t.Fatalf("response = %+v", resp)
	}
}

func TestChatRetriesOn429And5xx(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError} {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if attempts == 1 {
					if status == http.StatusTooManyRequests {
						w.Header().Set("Retry-After", "0")
					}
					w.WriteHeader(status)
					return
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
			}))
			defer server.Close()

			client := NewOpenAIClient(Config{BaseURL: server.URL, Model: "m", APIKey: "k", Timeout: 5 * time.Second})
			client.sleep = func(context.Context, time.Duration) error { return nil }

			resp, err := client.Chat(context.Background(), Request{})
			if err != nil {
				t.Fatal(err)
			}
			if attempts != 2 || resp.Message.Content != "ok" {
				t.Fatalf("attempts=%d resp=%+v", attempts, resp)
			}
		})
	}
}

func TestChatDoesNotRetry400(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad request"))
	}))
	defer server.Close()

	client := NewOpenAIClient(Config{BaseURL: server.URL, Model: "m", APIKey: "k", Timeout: 5 * time.Second})
	client.sleep = func(context.Context, time.Duration) error { return nil }

	_, err := client.Chat(context.Background(), Request{})
	if err == nil || !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("error = %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestChatRetriesNetworkErrors(t *testing.T) {
	transport := &countingTransport{}
	client := NewOpenAIClient(Config{BaseURL: "https://example.invalid", Model: "m", APIKey: "k", Timeout: time.Second})
	client.httpClient.Transport = transport
	client.sleep = func(context.Context, time.Duration) error { return nil }

	_, err := client.Chat(context.Background(), Request{})
	if err == nil {
		t.Fatal("expected error")
	}
	if transport.calls != maxAttempts {
		t.Fatalf("transport calls = %d, want %d", transport.calls, maxAttempts)
	}
}

type countingTransport struct {
	calls int
}

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls++
	return nil, &url.Error{Op: "Post", URL: "https://example.invalid", Err: errors.New("boom")}
}

func TestChatContextCancellation(t *testing.T) {
	client := NewOpenAIClient(Config{BaseURL: "https://example.invalid", Model: "m", APIKey: "k"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.Chat(ctx, Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestChatInvalidResponse(t *testing.T) {
	for name, body := range map[string]string{
		"invalid-json": "not json",
		"no-choices":   `{"choices":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			client := NewOpenAIClient(Config{BaseURL: server.URL, Model: "m", APIKey: "k", Timeout: 5 * time.Second})
			if _, err := client.Chat(context.Background(), Request{}); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("empty = %v", got)
	}
	if got := parseRetryAfter("2"); got != 2*time.Second {
		t.Errorf("seconds = %v", got)
	}
	if got := parseRetryAfter("-1"); got != 0 {
		t.Errorf("negative = %v", got)
	}
	if got := parseRetryAfter("garbage"); got != 0 {
		t.Errorf("garbage = %v", got)
	}
	future := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got <= 0 || got > 3*time.Second {
		t.Errorf("http date = %v", got)
	}
	if got := parseRetryAfter("999999"); got != maxRetryAfter {
		t.Errorf("large seconds = %v, want %v", got, maxRetryAfter)
	}
	if got := parseRetryAfter("99999999999999999999999"); got != maxRetryAfter {
		t.Errorf("overflow seconds = %v, want %v", got, maxRetryAfter)
	}
	farFuture := time.Now().Add(24 * time.Hour).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(farFuture); got != maxRetryAfter {
		t.Errorf("far future http date = %v, want %v", got, maxRetryAfter)
	}
}

func TestAPIKeyHandling(t *testing.T) {
	tempDir := t.TempDir()

	validKeyPath := filepath.Join(tempDir, "valid.key")
	if err := os.WriteFile(validKeyPath, []byte("valid-secret-key\n  "), 0600); err != nil {
		t.Fatal(err)
	}

	openPermPath := filepath.Join(tempDir, "open.key")
	if err := os.WriteFile(openPermPath, []byte("valid-secret-key\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(openPermPath, 0644)

	symlinkPath := filepath.Join(tempDir, "link.key")
	symlinkSupported := true
	if err := os.Symlink(validKeyPath, symlinkPath); err != nil {
		symlinkSupported = false
	}

	oversizePath := filepath.Join(tempDir, "oversize.key")
	if err := os.WriteFile(oversizePath, make([]byte, 4097), 0600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		envKey     string
		envFile    string
		wantKey    string
		wantErrMsg string
		skipOnWin  bool
	}{
		{
			name:    "key_from_env",
			envKey:  "valid-secret-key-123",
			wantKey: "valid-secret-key-123",
		},
		{
			name:    "key_from_file",
			envFile: validKeyPath,
			wantKey: "valid-secret-key",
		},
		{
			name:       "both_set_error",
			envKey:     "valid-secret-key-123",
			envFile:    validKeyPath,
			wantErrMsg: "both NABD_API_KEY and NABD_API_KEY_FILE are set",
		},
		{
			name:       "open_permissions_0644_error",
			envFile:    openPermPath,
			wantErrMsg: "too open",
			skipOnWin:  true,
		},
		{
			name:       "symlink_error",
			envFile:    symlinkPath,
			wantErrMsg: "symlink",
		},
		{
			name:       "oversize_error",
			envFile:    oversizePath,
			wantErrMsg: "exceeds maximum size",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipOnWin && runtime.GOOS == "windows" {
				t.Skip("skipping unix permission check on windows")
			}
			if tc.name == "symlink_error" && !symlinkSupported {
				t.Skip("symlink creation not supported")
			}

			t.Setenv("NABD_API_KEY", tc.envKey)
			t.Setenv("NABD_API_KEY_FILE", tc.envFile)
			t.Setenv("NABD_BASE_URL", "http://127.0.0.1:8080/v1")
			t.Setenv("NABD_MODEL", "local")

			cfg, err := ConfigFromEnv()
			if tc.wantErrMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrMsg) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErrMsg, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.APIKey.SecretValue() != tc.wantKey {
				t.Fatalf("got key %q, want %q", cfg.APIKey.SecretValue(), tc.wantKey)
			}
		})
	}
}

func TestAPIKeyFormatValidation(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"valid_alphanumeric", "sk-proj-12345678", false},
		{"valid_min_length_8", "12345678", false},
		{"too_short", "short", true},
		{"contains_crlf", "sk-proj\r\n1234", true},
		{"contains_newline", "sk-proj\n1234", true},
		{"contains_space", "sk-proj 1234", true},
		{"contains_tab", "sk-proj\t1234", true},
		{"non_ascii", "sk-proj-مفتاح", true},
		{"too_long", strings.Repeat("a", 513), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{
				BaseURL: DefaultBaseURL,
				Model:   DefaultModel,
				APIKey:  Secret(tc.key),
			}
			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for key %q, got nil", tc.key)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for key %q: %v", tc.key, err)
			}
		})
	}
}

func TestEmptyKeyLoopbackNoAuthHeader(t *testing.T) {
	var receivedAuth string
	var headerPresent bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, headerPresent = r.Header["Authorization"]
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`))
	}))
	defer server.Close()

	client := NewOpenAIClient(Config{
		BaseURL: server.URL,
		Model:   "local",
		APIKey:  "",
		Timeout: 5 * time.Second,
	})
	resp, err := client.Chat(context.Background(), Request{})
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if resp.Message.Content != "hello" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if headerPresent || receivedAuth != "" {
		t.Fatalf("expected no Authorization header on loopback with empty key, got %q", receivedAuth)
	}
}

func TestEmptyKeyRemoteHTTPSFailsValidate(t *testing.T) {
	cfg := Config{
		BaseURL: "https://api.openai.com/v1",
		Model:   "gpt-4",
		APIKey:  "",
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "remote endpoint requires an API key") {
		t.Fatalf("expected remote endpoint requires an API key error, got %v", err)
	}
}

func TestGenerationSettingsFromEnv(t *testing.T) {
	tests := []struct {
		name       string
		timeout    string
		maxTokens  string
		temp       string
		wantErrMsg string
		check      func(t *testing.T, cfg Config)
	}{
		{
			name:      "valid_settings",
			timeout:   "90s",
			maxTokens: "4096",
			temp:      "0.7",
			check: func(t *testing.T, cfg Config) {
				if cfg.Timeout != 90*time.Second {
					t.Fatalf("timeout = %v, want 90s", cfg.Timeout)
				}
				if cfg.MaxTokens != 4096 {
					t.Fatalf("max_tokens = %d, want 4096", cfg.MaxTokens)
				}
				if cfg.Temperature == nil || *cfg.Temperature != 0.7 {
					t.Fatalf("temperature = %v, want 0.7", cfg.Temperature)
				}
			},
		},
		{
			name:       "invalid_timeout_range_low",
			timeout:    "2s",
			wantErrMsg: "NABD_TIMEOUT",
		},
		{
			name:       "invalid_timeout_range_high",
			timeout:    "700s",
			wantErrMsg: "NABD_TIMEOUT",
		},
		{
			name:       "invalid_max_tokens_zero",
			maxTokens:  "0",
			wantErrMsg: "NABD_MAX_TOKENS",
		},
		{
			name:       "invalid_max_tokens_high",
			maxTokens:  "200000",
			wantErrMsg: "NABD_MAX_TOKENS",
		},
		{
			name:       "invalid_temperature_negative",
			temp:       "-0.1",
			wantErrMsg: "NABD_TEMPERATURE",
		},
		{
			name:       "invalid_temperature_high",
			temp:       "2.5",
			wantErrMsg: "NABD_TEMPERATURE",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NABD_TIMEOUT", tc.timeout)
			t.Setenv("NABD_MAX_TOKENS", tc.maxTokens)
			t.Setenv("NABD_TEMPERATURE", tc.temp)
			t.Setenv("NABD_BASE_URL", DefaultBaseURL)
			t.Setenv("NABD_MODEL", DefaultModel)
			t.Setenv("NABD_API_KEY", "")
			t.Setenv("NABD_API_KEY_FILE", "")

			cfg, err := ConfigFromEnv()
			if tc.wantErrMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrMsg) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErrMsg, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, cfg)
			}
		})
	}
}

func TestTemperatureZeroSentInPayload(t *testing.T) {
	t.Setenv("NABD_TEMPERATURE", "0")
	t.Setenv("NABD_BASE_URL", DefaultBaseURL)
	t.Setenv("NABD_MODEL", DefaultModel)
	t.Setenv("NABD_API_KEY", "")
	t.Setenv("NABD_API_KEY_FILE", "")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv failed: %v", err)
	}
	if cfg.Temperature == nil || *cfg.Temperature != 0.0 {
		t.Fatalf("expected temperature 0.0, got %v", cfg.Temperature)
	}

	var seen struct {
		Temperature *float64 `json:"temperature"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Errorf("decode error: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	cfg.BaseURL = server.URL
	client := NewOpenAIClient(cfg)
	_, err = client.Chat(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if seen.Temperature == nil || *seen.Temperature != 0.0 {
		t.Fatalf("payload temperature = %v, want 0.0", seen.Temperature)
	}
}

func TestErrorScrubbingRedactsKeyAndSubstrings(t *testing.T) {
	apiKey := "super-secret-token-12345678"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Error: rejected key " + apiKey + " and fragment super-secret-token"))
	}))
	defer server.Close()

	client := NewOpenAIClient(Config{
		BaseURL: server.URL,
		Model:   "test-model",
		APIKey:  Secret(apiKey),
		Timeout: 5 * time.Second,
	})
	client.sleep = func(context.Context, time.Duration) error { return nil }
	_, err := client.Chat(context.Background(), Request{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	errStr := err.Error()
	if strings.Contains(errStr, apiKey) {
		t.Fatalf("error contains raw API key: %s", errStr)
	}
	if strings.Contains(errStr, "super-secret-token") {
		t.Fatalf("error contains key substring: %s", errStr)
	}
	if !strings.Contains(errStr, "[REDACTED]") {
		t.Fatalf("error does not contain [REDACTED]: %s", errStr)
	}
}

func TestErrorScrubbingLargeBody(t *testing.T) {
	apiKey := "super-secret-token-12345678"
	body := "Error: " + apiKey + strings.Repeat("A", 128*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := NewOpenAIClient(Config{
		BaseURL: server.URL,
		Model:   "test-model",
		APIKey:  Secret(apiKey),
		Timeout: 5 * time.Second,
	})
	client.sleep = func(context.Context, time.Duration) error { return nil }
	_, err := client.Chat(context.Background(), Request{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	errStr := err.Error()
	if strings.Contains(errStr, apiKey) {
		t.Fatalf("error contains raw API key: %s", errStr)
	}
	if !strings.Contains(errStr, "[REDACTED]") {
		t.Fatalf("error does not contain [REDACTED]: %s", errStr)
	}
}

func TestSecretFormatting(t *testing.T) {
	keyVal := "sk-super-secret-api-key-987654321"
	cfg := Config{
		BaseURL: DefaultBaseURL,
		Model:   DefaultModel,
		APIKey:  Secret(keyVal),
	}
	str := fmt.Sprintf("%v %+v %#v", cfg, cfg, cfg)
	if strings.Contains(str, keyVal) {
		t.Fatalf("fmt.Sprintf exposed secret: %s", str)
	}
	if !strings.Contains(str, "[REDACTED]") {
		t.Fatalf("fmt.Sprintf missing [REDACTED]: %s", str)
	}

	jsonData, err := json.Marshal(cfg.APIKey)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(jsonData), keyVal) {
		t.Fatalf("json.Marshal exposed secret: %s", jsonData)
	}
	if string(jsonData) != `"[REDACTED]"` {
		t.Fatalf("json.Marshal = %s, want %q", jsonData, `"[REDACTED]"`)
	}

	textData, err := cfg.APIKey.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(textData), keyVal) {
		t.Fatalf("MarshalText exposed secret: %s", textData)
	}
	if string(textData) != "[REDACTED]" {
		t.Fatalf("MarshalText = %s, want %q", textData, "[REDACTED]")
	}
}

func TestCheckModel(t *testing.T) {
	tests := []struct {
		name        string
		targetModel string
		handler     http.HandlerFunc
		wantErrMsg  string
		wantNoErr   bool
	}{
		{
			name:        "model_exists",
			targetModel: "gpt-4o",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`))
			},
			wantNoErr: true,
		},
		{
			name:        "model_missing_with_suggestions",
			targetModel: "gpt-4",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"},{"id":"gemini-1.5"}]}`))
			},
			wantErrMsg: "did you mean: gpt-4o, gpt-4o-mini, gemini-1.5?",
		},
		{
			name:        "model_404_skips_without_failure",
			targetModel: "gpt-4o",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantNoErr: true,
		},
		{
			name:        "model_redirect_rejected",
			targetModel: "gpt-4o",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/other", http.StatusFound)
			},
			wantErrMsg: "redirect",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()

			client := NewOpenAIClient(Config{
				BaseURL: server.URL,
				Model:   tc.targetModel,
				APIKey:  "valid-secret-key",
			})

			err := client.CheckModel(context.Background())
			if tc.wantNoErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErrMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrMsg) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErrMsg, err)
				}
			}
		})
	}
}

func TestUnauthorizedErrors(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"bad api key"}`))
			}))
			defer server.Close()

			client := NewOpenAIClient(Config{
				BaseURL: server.URL,
				Model:   "test-model",
				APIKey:  "secret-key-123",
				Timeout: 5 * time.Second,
			})
			_, err := client.Chat(context.Background(), Request{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("expected ErrUnauthorized, got %v", err)
			}
			if attempts != 1 {
				t.Fatalf("expected 1 attempt (no retry), got %d", attempts)
			}
		})
	}
}

type staticLoopbackResolver struct {
	ips []net.IPAddr
	err error
}

func (r staticLoopbackResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.ips, r.err
}

func TestOpenAICheckModelRejectsInsecureBaseURL(t *testing.T) {
	for _, rawURL := range []string{
		"http://example.com/v1",
		"http://169.254.169.254/v1",
		"ftp://127.0.0.1:8080/v1",
		"://invalid-url",
	} {
		t.Run(rawURL, func(t *testing.T) {
			client := NewOpenAIClient(Config{
				BaseURL: rawURL,
				Model:   "gpt-4o",
				APIKey:  "secret-key-123",
			})
			if err := client.CheckModel(context.Background()); err == nil {
				t.Fatalf("expected CheckModel to reject %q, got nil", rawURL)
			}
		})
	}
}

func TestOpenAILoopbackDialerRejectsNonLoopbackIP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"data":[{"id":"local"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	localhostURL := "http://localhost:" + u.Port() + "/v1"

	// 1. Non-loopback IP returned for localhost must fail closed in both Chat and CheckModel.
	badClient := NewOpenAIClient(Config{
		BaseURL: localhostURL,
		Model:   "local",
		Timeout: 2 * time.Second,
	})
	badClient.resolver = staticLoopbackResolver{ips: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}}
	if _, err := badClient.Chat(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}}); !errors.Is(err, errNonLoopbackAddress) {
		t.Fatalf("expected Chat to reject non-loopback IP, got: %v", err)
	}
	if err := badClient.CheckModel(context.Background()); !errors.Is(err, errNonLoopbackAddress) {
		t.Fatalf("expected CheckModel to reject non-loopback IP, got: %v", err)
	}

	// 2. Mixed loopback + non-loopback resolution must also fail closed.
	badClient.resolver = staticLoopbackResolver{ips: []net.IPAddr{
		{IP: net.ParseIP("127.0.0.1")},
		{IP: net.ParseIP("10.0.0.1")},
	}}
	if _, err := badClient.Chat(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}}); !errors.Is(err, errNonLoopbackAddress) {
		t.Fatalf("expected Chat to reject mixed non-loopback DNS response, got: %v", err)
	}

	// 3. Verified loopback IP succeeds.
	goodClient := NewOpenAIClient(Config{
		BaseURL: localhostURL,
		Model:   "local",
		Timeout: 2 * time.Second,
	})
	goodClient.resolver = staticLoopbackResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	if err := goodClient.CheckModel(context.Background()); err != nil {
		t.Fatalf("expected verified loopback CheckModel to succeed, got: %v", err)
	}
	if _, err := goodClient.Chat(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("expected verified loopback Chat to succeed, got: %v", err)
	}
}
