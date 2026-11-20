package llm

import (
	"bufio"
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	mrand "math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxAttempts      = 4
	maxRetryAfter    = 30 * time.Second
	maxResponseBytes = 4 << 20
)

// OpenAIClient talks to an OpenAI-compatible /chat/completions endpoint.
type OpenAIClient struct {
	cfg         Config
	httpClient  *http.Client
	sleep       func(context.Context, time.Duration) error
	backoffBase time.Duration
}

func NewOpenAIClient(cfg Config) *OpenAIClient {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.APIKey == "" {
		cfg.APIKey = "sk-no-key"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = DefaultMaxTokens
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = DefaultTemperature
	}

	// HTTP/2-capable transport with connection reuse to avoid TCP/TLS
	// handshakes on every request cycle in Termux.
	tr := &http.Transport{
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
	}
	return &OpenAIClient{
		cfg:         cfg,
		httpClient:  &http.Client{Timeout: cfg.Timeout, Transport: tr},
		sleep:       sleepContext,
		backoffBase: 500 * time.Millisecond,
	}
}

// Chat sends a blocking (non-streaming) chat completion request with retries.
func (c *OpenAIClient) Chat(ctx context.Context, req Request) (*Response, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	body, err := c.encode(req, false)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"

	var lastErr error
	var wait time.Duration
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if wait <= 0 {
				wait = c.backoff(attempt)
			}
			if err := c.sleep(ctx, wait); err != nil {
				return nil, err
			}
			wait = 0
		}

		response, retryable, retryAfter, err := c.attempt(ctx, endpoint, body)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !retryable {
			return nil, err
		}
		if retryAfter > maxRetryAfter {
			retryAfter = maxRetryAfter
		}
		wait = retryAfter
	}
	return nil, fmt.Errorf("after %d attempts: %w", maxAttempts, lastErr)
}

// ChatStream sends a streaming chat completion request. onDelta receives each
// incremental text or tool-call fragment as the server streams it. The fully
// assembled Response is returned when the stream ends. Retries are NOT applied
// to streaming requests because partial output cannot be safely replayed.
//
// If onDelta is nil the method still assembles the full response (useful when
// the caller only wants the final result without live updates).
func (c *OpenAIClient) ChatStream(ctx context.Context, req Request, onDelta func(StreamDelta)) (*Response, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	body, err := c.encode(req, true)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, clipText(string(data), 300))
	}

	return c.readStream(ctx, resp.Body, onDelta)
}

// readStream reads a text/event-stream response body, assembles the full
// response, and fires onDelta for each incremental piece.
func (c *OpenAIClient) readStream(ctx context.Context, body io.Reader, onDelta func(StreamDelta)) (*Response, error) {
	// Per-index accumulator for tool calls being streamed in fragments.
	type tcAccum struct {
		id        string
		name      string
		arguments strings.Builder
	}
	tcMap := map[int]*tcAccum{}

	var textBuilder strings.Builder
	var finishReason string
	var usage Usage

	scanner := bufio.NewScanner(io.LimitReader(body, maxResponseBytes))
	scanner.Buffer(make([]byte, 64*1024), maxResponseBytes)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		if data == "" {
			continue
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage Usage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // malformed chunk — skip, don't abort
		}
		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
			if content := choice.Delta.Content; content != "" {
				textBuilder.WriteString(content)
				if onDelta != nil {
					onDelta(StreamDelta{Content: content})
				}
			}
			for _, tc := range choice.Delta.ToolCalls {
				acc, ok := tcMap[tc.Index]
				if !ok {
					acc = &tcAccum{}
					tcMap[tc.Index] = acc
				}
				if tc.ID != "" && acc.id == "" {
					acc.id = tc.ID
				}
				if tc.Function.Name != "" && acc.name == "" {
					acc.name = tc.Function.Name
				}
				acc.arguments.WriteString(tc.Function.Arguments)
				if onDelta != nil {
					onDelta(StreamDelta{ToolCallDelta: &ToolCallDelta{
						Index:     tc.Index,
						ID:        tc.ID,
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					}})
				}
			}
		}
		if chunk.Usage.TotalTokens > 0 {
			usage = chunk.Usage
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}

	// Assemble the full message from accumulated fragments.
	msg := Message{Role: "assistant", Content: textBuilder.String()}

	// Build ordered tool calls from the index map.
	indices := make([]int, 0, len(tcMap))
	for idx := range tcMap {
		indices = append(indices, idx)
	}
	// Sort indices ascending.
	for i := 0; i < len(indices); i++ {
		for j := i + 1; j < len(indices); j++ {
			if indices[i] > indices[j] {
				indices[i], indices[j] = indices[j], indices[i]
			}
		}
	}
	for i, idx := range indices {
		acc := tcMap[idx]
		id := acc.id
		if id == "" {
			id = newCallID(i)
		}
		var tc ToolCall
		tc.ID = id
		tc.Type = "function"
		tc.Function.Name = acc.name
		tc.Function.Arguments = acc.arguments.String()
		msg.ToolCalls = append(msg.ToolCalls, tc)
	}

	return &Response{Message: msg, FinishReason: finishReason, Usage: usage}, nil
}

func (c *OpenAIClient) encode(req Request, stream bool) ([]byte, error) {
	payload := map[string]any{
		"model":       c.cfg.Model,
		"messages":    req.Messages,
		"temperature": c.cfg.Temperature,
		"max_tokens":  c.cfg.MaxTokens,
	}
	if stream {
		payload["stream"] = true
	}
	if len(req.Tools) > 0 {
		payload["tools"] = req.Tools
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return body, nil
}

func (c *OpenAIClient) attempt(ctx context.Context, endpoint string, body []byte) (*Response, bool, time.Duration, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, 0, ctx.Err()
		}
		return nil, true, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, true, 0, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		delay := parseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, retryable, delay, fmt.Errorf("status %d: %s", resp.StatusCode, clipText(string(data), 300))
	}

	parsed, err := parseResponse(data)
	if err != nil {
		return nil, false, 0, err
	}
	return parsed, false, 0, nil
}

func parseResponse(data []byte) (*Response, error) {
	var result struct {
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("response has no choices")
	}

	choice := result.Choices[0]
	msg := choice.Message
	if msg.Role == "" {
		msg.Role = "assistant"
	}
	for i := range msg.ToolCalls {
		if msg.ToolCalls[i].ID == "" {
			msg.ToolCalls[i].ID = newCallID(i)
		}
		if msg.ToolCalls[i].Type == "" {
			msg.ToolCalls[i].Type = "function"
		}
	}
	return &Response{Message: msg, FinishReason: choice.FinishReason, Usage: result.Usage}, nil
}

func (c *OpenAIClient) backoff(attempt int) time.Duration {
	base := c.backoffBase * time.Duration(1<<uint(attempt))
	if base <= 0 {
		base = time.Second
	}
	if base > 10*time.Second {
		base = 10 * time.Second
	}
	return time.Duration(mrand.Int63n(int64(base)))
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}

func newCallID(index int) string {
	buf := make([]byte, 4)
	if _, err := crand.Read(buf); err != nil {
		return fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), index+1)
	}
	return fmt.Sprintf("call_%s_%d", hex.EncodeToString(buf), index+1)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func clipText(s string, n int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
