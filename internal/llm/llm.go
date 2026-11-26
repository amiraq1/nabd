// Package llm defines the chat client used by the agent and an
// OpenAI-compatible implementation.
package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/amiraq1/nabd/internal/safefs"
)

const (
	DefaultBaseURL       = "http://127.0.0.1:8080/v1"
	DefaultGeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"
	DefaultModel         = "local"
	DefaultGeminiModel   = "gemini-2.5-flash"
	DefaultTimeout       = 180 * time.Second
	DefaultMaxTokens     = 2048
	DefaultTemperature   = 0.2
	ProviderOpenAI       = "openai"
	ProviderGemini       = "gemini"
)

// Message is one chat message in OpenAI wire format.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Display    string     `json:"-"`
}

// ToolCall is one function call requested by the model.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

// Request is a single chat completion request.
type Request struct {
	Messages []Message
	Tools    []map[string]any
}

// Usage reports token counts when the server provides them.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Response is a single chat completion response.
type Response struct {
	Message      Message
	FinishReason string
	Usage        Usage
}

// Client is the interface the agent loop depends on.
type Client interface {
	Chat(ctx context.Context, req Request) (*Response, error)
}

// Secret wraps a sensitive string to prevent accidental disclosure
// in logs, error messages, and format strings.
type Secret string

func (s Secret) String() string {
	return "[REDACTED]"
}

func (s Secret) GoString() string {
	return "[REDACTED]"
}

func (s Secret) MarshalJSON() ([]byte, error) {
	return []byte(`"[REDACTED]"`), nil
}

func (s Secret) MarshalText() ([]byte, error) {
	return []byte("[REDACTED]"), nil
}

// SecretValue returns the underlying secret string for internal use.
func (s Secret) SecretValue() string {
	return string(s)
}

// Config holds endpoint and generation settings. Values come from the
// environment, never from command-line arguments.
type Config struct {
	Provider     string
	BaseURL      string
	Model        string
	APIKey       Secret
	Timeout      time.Duration
	MaxTokens    int
	Temperature  *float64
	GeminiSearch bool
}

// GetTemperature returns the configured temperature or the default (0.2).
func (c Config) GetTemperature() float64 {
	if c.Temperature != nil {
		return *c.Temperature
	}
	return DefaultTemperature
}

// ConfigFromEnv reads configuration from environment variables.
func ConfigFromEnv() (Config, error) {
	var errs []error

	providerRaw := strings.ToLower(strings.TrimSpace(os.Getenv("NABD_PROVIDER")))
	if providerRaw == "" {
		providerRaw = ProviderOpenAI
	}

	defaultBaseURL := DefaultBaseURL
	defaultModel := DefaultModel
	if providerRaw == ProviderGemini {
		defaultBaseURL = DefaultGeminiBaseURL
		defaultModel = DefaultGeminiModel
	}

	baseURL := getEnv("NABD_BASE_URL", defaultBaseURL)
	model := getEnv("NABD_MODEL", defaultModel)

	// OpenAI keys
	apiKeyVal := strings.TrimSpace(os.Getenv("NABD_API_KEY"))
	apiKeyFile := strings.TrimSpace(os.Getenv("NABD_API_KEY_FILE"))

	var openAIKey Secret
	if apiKeyVal != "" && apiKeyFile != "" {
		errs = append(errs, errors.New("both NABD_API_KEY and NABD_API_KEY_FILE are set; specify only one"))
	} else if apiKeyFile != "" {
		key, err := readAPIKeyFile(apiKeyFile)
		if err != nil {
			errs = append(errs, err)
		} else {
			openAIKey = Secret(key)
		}
	} else if apiKeyVal != "" {
		openAIKey = Secret(apiKeyVal)
	}

	// Gemini keys
	geminiKeyVal := strings.TrimSpace(os.Getenv("NABD_GEMINI_API_KEY"))
	geminiKeyFile := strings.TrimSpace(os.Getenv("NABD_GEMINI_API_KEY_FILE"))

	var geminiKey Secret
	if geminiKeyVal != "" && geminiKeyFile != "" {
		errs = append(errs, errors.New("both NABD_GEMINI_API_KEY and NABD_GEMINI_API_KEY_FILE are set; specify only one"))
	} else if geminiKeyFile != "" {
		key, err := readAPIKeyFile(geminiKeyFile)
		if err != nil {
			errs = append(errs, err)
		} else {
			geminiKey = Secret(key)
		}
	} else if geminiKeyVal != "" {
		geminiKey = Secret(geminiKeyVal)
	}

	// Select key based on provider with fallback
	var apiKey Secret
	if providerRaw == ProviderGemini {
		if geminiKey != "" {
			apiKey = geminiKey
		} else {
			apiKey = openAIKey
		}
	} else {
		apiKey = openAIKey
	}

	// Gemini search grounding
	var geminiSearch bool
	if v := strings.TrimSpace(os.Getenv("NABD_GEMINI_SEARCH")); v != "" {
		if v == "1" {
			geminiSearch = true
		} else if v == "0" {
			geminiSearch = false
		} else {
			errs = append(errs, fmt.Errorf("invalid NABD_GEMINI_SEARCH %q: must be 1, 0, or empty", v))
		}
	}

	timeout := DefaultTimeout
	if v := strings.TrimSpace(os.Getenv("NABD_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 5*time.Second || d > 600*time.Second {
			errs = append(errs, fmt.Errorf("invalid NABD_TIMEOUT %q: allowed range 5s-600s", v))
		} else {
			timeout = d
		}
	}

	maxTokens := DefaultMaxTokens
	if v := strings.TrimSpace(os.Getenv("NABD_MAX_TOKENS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 131072 {
			errs = append(errs, fmt.Errorf("invalid NABD_MAX_TOKENS %q: allowed range 1-131072", v))
		} else {
			maxTokens = n
		}
	}

	defaultTemp := DefaultTemperature
	temp := &defaultTemp
	if v := strings.TrimSpace(os.Getenv("NABD_TEMPERATURE")); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 2 {
			errs = append(errs, fmt.Errorf("invalid NABD_TEMPERATURE %q: allowed range 0-2", v))
		} else {
			temp = &f
		}
	}

	cfg := Config{
		Provider:     providerRaw,
		BaseURL:      baseURL,
		Model:        model,
		APIKey:       apiKey,
		Timeout:      timeout,
		MaxTokens:    maxTokens,
		Temperature:  temp,
		GeminiSearch: geminiSearch,
	}

	if len(errs) > 0 {
		return cfg, errors.Join(errs...)
	}
	return cfg, nil
}

// Validate checks all configuration values, returning all detected errors.
func (c Config) Validate() error {
	var errs []error

	provider := strings.ToLower(strings.TrimSpace(c.Provider))
	if provider == "" {
		provider = ProviderOpenAI
	}
	if provider != ProviderOpenAI && provider != ProviderGemini {
		errs = append(errs, fmt.Errorf("invalid NABD_PROVIDER %q: must be openai or gemini", c.Provider))
	}

	if err := validateBaseURL(c.BaseURL); err != nil {
		errs = append(errs, err)
	}

	if err := validateModel(c.Model); err != nil {
		errs = append(errs, err)
	}

	isLoopback := isLoopbackURL(c.BaseURL)
	if c.APIKey == "" {
		if !isLoopback {
			errs = append(errs, errors.New("remote endpoint requires an API key"))
		}
	} else {
		if err := validateKeyFormat(string(c.APIKey)); err != nil {
			errs = append(errs, err)
		}
	}

	if c.Timeout != 0 && (c.Timeout < 5*time.Second || c.Timeout > 600*time.Second) {
		errs = append(errs, fmt.Errorf("invalid timeout %v: allowed range 5s-600s", c.Timeout))
	}

	if c.MaxTokens != 0 && (c.MaxTokens < 1 || c.MaxTokens > 131072) {
		errs = append(errs, fmt.Errorf("invalid max_tokens %d: allowed range 1-131072", c.MaxTokens))
	}

	if c.Temperature != nil && (*c.Temperature < 0 || *c.Temperature > 2) {
		errs = append(errs, fmt.Errorf("invalid temperature %v: allowed range 0-2", *c.Temperature))
	}

	return errors.Join(errs...)
}

// NewClient returns a configured Client based on cfg.Provider.
func NewClient(cfg Config) (Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch provider {
	case ProviderGemini:
		return NewGeminiClient(cfg), nil
	case ProviderOpenAI, "":
		return NewOpenAIClient(cfg), nil
	default:
		return nil, fmt.Errorf("unknown provider %q", cfg.Provider)
	}
}

func readAPIKeyFile(path string) (string, error) {
	data, _, err := safefs.ReadFileNoFollow(path, safefs.OpenPolicy{
		MaxBytes:          4096,
		RequireOwner:      true,
		StrictPrivateMode: true,
		RejectExecutable:  true,
	})
	if err != nil {
		switch {
		case errors.Is(err, safefs.ErrSymlink):
			return "", fmt.Errorf("NABD_API_KEY_FILE cannot be a symlink: %s", path)
		case errors.Is(err, safefs.ErrNotRegularFile), errors.Is(err, safefs.ErrNotDirectory):
			return "", fmt.Errorf("NABD_API_KEY_FILE must be a regular file: %s", path)
		case errors.Is(err, safefs.ErrTooLarge):
			return "", fmt.Errorf("NABD_API_KEY_FILE exceeds maximum size of 4KiB (%w)", err)
		case errors.Is(err, safefs.ErrPermission):
			return "", fmt.Errorf("NABD_API_KEY_FILE permissions are too open (must be 0600 or less): %w", err)
		case errors.Is(err, safefs.ErrOwner):
			return "", fmt.Errorf("NABD_API_KEY_FILE owner invalid: %w", err)
		default:
			return "", fmt.Errorf("read NABD_API_KEY_FILE: %w", err)
		}
	}
	s := strings.TrimRight(string(data), "\r\n")
	s = strings.TrimSpace(s)
	return s, nil
}

func validateKeyFormat(key string) error {
	if len(key) < 8 || len(key) > 512 {
		return fmt.Errorf("API key length %d is invalid (must be between 8 and 512 characters)", len(key))
	}
	for i := 0; i < len(key); i++ {
		b := key[i]
		if b < 33 || b > 126 {
			return fmt.Errorf("API key contains invalid character at index %d (only printable ASCII without spaces or control characters allowed)", i)
		}
	}
	return nil
}

func validateModel(name string) error {
	if name == "" {
		return errors.New("NABD_MODEL cannot be empty")
	}
	if len(name) > 256 {
		return fmt.Errorf("NABD_MODEL exceeds maximum length of 256 characters (%d chars)", len(name))
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		switch {
		case b >= 'a' && b <= 'z':
		case b >= 'A' && b <= 'Z':
		case b >= '0' && b <= '9':
		case b == '.' || b == '_' || b == ':' || b == '/' || b == '@' || b == '-':
		default:
			return fmt.Errorf("NABD_MODEL contains invalid character %q (only [A-Za-z0-9._:/@-] allowed)", string(b))
		}
	}
	return nil
}

func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func getEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
