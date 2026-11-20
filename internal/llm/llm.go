// Package llm defines the chat client used by the agent and an
// OpenAI-compatible implementation.
package llm

import (
	"context"
	"os"
	"strings"
	"time"
)

const (
	DefaultBaseURL     = "http://127.0.0.1:8080/v1"
	DefaultModel       = "local"
	DefaultTimeout     = 180 * time.Second
	DefaultMaxTokens   = 2048
	DefaultTemperature = 0.2
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

// StreamDelta is one incremental piece of a streaming response.
// Either Content is non-empty (a text token) or ToolCallDelta is set
// (a partial tool_call fragment at the given Index).
type StreamDelta struct {
	Content       string
	ToolCallDelta *ToolCallDelta
}

// ToolCallDelta is a partial tool call fragment delivered by the stream.
type ToolCallDelta struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

// Client is the interface the agent loop depends on.
type Client interface {
	// Chat sends a blocking request and returns the full response.
	Chat(ctx context.Context, req Request) (*Response, error)

	// ChatStream sends a streaming request. onDelta is called for every
	// incremental token or tool-call fragment in arrival order. The final
	// assembled Response is returned when the stream ends. If onDelta is nil
	// the method must still work (behaves like Chat with streaming).
	ChatStream(ctx context.Context, req Request, onDelta func(StreamDelta)) (*Response, error)
}

// Config holds endpoint and generation settings. Values come from the
// environment, never from command-line arguments.
type Config struct {
	BaseURL     string
	Model       string
	APIKey      string
	Timeout     time.Duration
	MaxTokens   int
	Temperature float64
}

// ConfigFromEnv reads NABD_BASE_URL, NABD_MODEL and NABD_API_KEY.
func ConfigFromEnv() Config {
	return Config{
		BaseURL:     getEnv("NABD_BASE_URL", DefaultBaseURL),
		Model:       getEnv("NABD_MODEL", DefaultModel),
		APIKey:      getEnv("NABD_API_KEY", "sk-no-key"),
		Timeout:     DefaultTimeout,
		MaxTokens:   DefaultMaxTokens,
		Temperature: DefaultTemperature,
	}
}

func getEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
