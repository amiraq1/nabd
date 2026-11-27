package llm

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrUnauthorized = errors.New("المفتاح مرفوض من المزوّد")

const (
	maxAttempts        = 4
	maxRetryAfter      = 30 * time.Second
	maxResponseBytes   = 4 << 20
	maxToolCalls       = 64
	maxErrorScrubBytes = 64 << 10
)

// OpenAIClient talks to an OpenAI-compatible /chat/completions endpoint.
type OpenAIClient struct {
	cfg         Config
	httpClient  *http.Client
	resolver    loopbackResolver
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
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = DefaultMaxTokens
	}
	if cfg.Temperature == nil {
		t := DefaultTemperature
		cfg.Temperature = &t
	}
	c := &OpenAIClient{
		cfg:         cfg,
		sleep:       sleepContext,
		backoffBase: 500 * time.Millisecond,
	}
	c.httpClient = newLLMHTTPClient(cfg.BaseURL, cfg.Timeout, func() loopbackResolver { return c.resolver })
	return c
}

// BaseURL identifies the model endpoint for disclosure prompts.
func (c *OpenAIClient) BaseURL() string { return c.cfg.BaseURL }

// Model identifies the configured model name.
func (c *OpenAIClient) Model() string { return c.cfg.Model }

// HasKey reports whether an API key is configured.
func (c *OpenAIClient) HasKey() bool { return c.cfg.APIKey != "" }

func (c *OpenAIClient) Chat(ctx context.Context, req Request) (*Response, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	if err := validateBaseURL(c.cfg.BaseURL); err != nil {
		return nil, err
	}
	ctx = withSchemeDialPolicy(ctx, c.cfg.BaseURL)
	body, err := c.encode(req)
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

// validateBaseURL avoids sending credentials over insecure remote connections.
func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid model base URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return fmt.Errorf("model base URL must use HTTPS or local loopback HTTP")
}

func (c *OpenAIClient) encode(req Request) ([]byte, error) {
	payload := map[string]any{
		"model":       c.cfg.Model,
		"messages":    req.Messages,
		"temperature": c.cfg.GetTemperature(),
		"max_tokens":  c.cfg.MaxTokens,
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
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey.SecretValue())
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, 0, ctx.Err()
		}
		if errors.Is(err, errNonLoopbackAddress) {
			return nil, false, 0, err
		}
		return nil, true, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, true, 0, fmt.Errorf("read response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return nil, false, 0, fmt.Errorf("response too large: exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		errBytes := data
		if len(errBytes) > maxErrorScrubBytes {
			errBytes = errBytes[:maxErrorScrubBytes]
		}
		rawBody := string(errBytes)
		if c.cfg.APIKey != "" {
			rawBody = redactSecret(rawBody, c.cfg.APIKey.SecretValue())
		}
		clipped := clipText(rawBody, 300)

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, false, 0, fmt.Errorf("%w (status %d): %s", ErrUnauthorized, resp.StatusCode, clipped)
		}

		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		delay := parseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, retryable, delay, fmt.Errorf("status %d: %s", resp.StatusCode, clipped)
	}

	parsed, err := parseResponse(data)
	if err != nil {
		return nil, false, 0, err
	}
	return parsed, false, 0, nil
}

// CheckModel verifies that cfg.Model is present in GET {base}/models.
// If the provider returns 404, it prints a warning to stderr and skips without failure.
func (c *OpenAIClient) CheckModel(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := validateBaseURL(c.cfg.BaseURL); err != nil {
		return err
	}
	ctx = withSchemeDialPolicy(ctx, c.cfg.BaseURL)

	endpoint := strings.TrimRight(c.cfg.BaseURL, "/") + "/models"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create check model request: %w", err)
	}
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey.SecretValue())
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("model check request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		fmt.Fprintf(os.Stderr, "Warning: model check endpoint %s returned 404; skipping check\n", endpoint)
		return nil
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return fmt.Errorf("model check redirected with status %d (redirects are not allowed)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("model check returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read model check response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("model check response too large: exceeds %d bytes", maxResponseBytes)
	}

	var modelsResp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &modelsResp); err != nil {
		return fmt.Errorf("decode models response: %w", err)
	}

	var names []string
	for _, item := range modelsResp.Data {
		id := strings.TrimSpace(item.ID)
		if id != "" {
			if id == c.cfg.Model {
				return nil
			}
			names = append(names, id)
		}
	}

	suggestions := closestNames(c.cfg.Model, names, 3)
	if len(suggestions) > 0 {
		return fmt.Errorf("model %q not found; did you mean: %s?", c.cfg.Model, strings.Join(suggestions, ", "))
	}
	return fmt.Errorf("model %q not found on provider", c.cfg.Model)
}

// CheckModel is a convenience function that verifies the model using a temporary client.
func CheckModel(ctx context.Context, cfg Config) error {
	return NewOpenAIClient(cfg).CheckModel(ctx)
}

func closestNames(target string, candidates []string, maxN int) []string {
	if len(candidates) == 0 || maxN <= 0 {
		return nil
	}
	type item struct {
		name string
		dist int
	}
	items := make([]item, len(candidates))
	for i, c := range candidates {
		items[i] = item{name: c, dist: levenshtein(target, c)}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].dist != items[j].dist {
			return items[i].dist < items[j].dist
		}
		return items[i].name < items[j].name
	})
	n := maxN
	if len(items) < n {
		n = len(items)
	}
	res := make([]string, n)
	for i := 0; i < n; i++ {
		res[i] = items[i].name
	}
	return res
}

func levenshtein(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := 0; j <= len(b); j++ {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, min(d[i][j-1]+1, d[i-1][j-1]+cost))
		}
	}
	return d[len(a)][len(b)]
}

func redactSecret(text, secret string) string {
	if secret == "" || text == "" {
		return text
	}
	text = strings.ReplaceAll(text, secret, "[REDACTED]")
	if len(secret) < 8 || len(text) < 8 {
		return text
	}

	eightGrams := make(map[string]struct{}, len(secret)-7)
	for i := 0; i <= len(secret)-8; i++ {
		eightGrams[secret[i:i+8]] = struct{}{}
	}

	var b strings.Builder
	b.Grow(len(text))
	i := 0
	for i < len(text) {
		if i+8 <= len(text) {
			if _, ok := eightGrams[text[i:i+8]]; ok {
				j := i + 8
				for j < len(text) && strings.Contains(secret, text[i:j+1]) {
					j++
				}
				b.WriteString("[REDACTED]")
				i = j
				continue
			}
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

// openAIUsageWire keeps every counter optional so a sparse or malformed usage
// object can be diagnosed without failing an otherwise successful completion.
type openAIUsageWire struct {
	PromptTokens         json.Number `json:"prompt_tokens"`
	CompletionTokens     json.Number `json:"completion_tokens"`
	TotalTokens          json.Number `json:"total_tokens"`
	PromptCacheHitTokens json.Number `json:"prompt_cache_hit_tokens"`
	PromptTokensDetails  *struct {
		CachedTokens json.Number `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func parseOpenAIUsage(raw json.RawMessage) Usage {
	if len(raw) == 0 || string(raw) == "null" {
		return Usage{}
	}
	var wire openAIUsageWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Usage{Reported: true, Invalid: true}
	}
	usage := Usage{Reported: true}
	var ok bool
	usage.PromptTokens, usage.PromptReported, ok = parseTokenCount(wire.PromptTokens)
	if !ok {
		usage.Invalid = true
	}
	usage.CompletionTokens, _, ok = parseTokenCount(wire.CompletionTokens)
	if !ok {
		usage.Invalid = true
	}
	usage.TotalTokens, _, ok = parseTokenCount(wire.TotalTokens)
	if !ok {
		usage.Invalid = true
	}
	if wire.PromptTokensDetails != nil {
		if value, present, valid := parseTokenCount(wire.PromptTokensDetails.CachedTokens); present {
			if !valid {
				usage.Invalid = true
			} else {
				usage.CachedPromptTokens = &value
			}
		}
	}
	if value, present, valid := parseTokenCount(wire.PromptCacheHitTokens); present {
		if !valid {
			usage.Invalid = true
		} else {
			usage.PromptCacheHitTokens = &value
		}
	}
	if usage.CachedPromptTokens != nil && usage.PromptCacheHitTokens != nil &&
		*usage.CachedPromptTokens != *usage.PromptCacheHitTokens {
		usage.Invalid = true
	}
	if usage.PromptReported {
		if usage.CachedPromptTokens != nil && *usage.CachedPromptTokens > usage.PromptTokens {
			usage.Invalid = true
		}
		if usage.PromptCacheHitTokens != nil && *usage.PromptCacheHitTokens > usage.PromptTokens {
			usage.Invalid = true
		}
	}
	return usage
}

func parseResponse(data []byte) (*Response, error) {
	var result struct {
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
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
	if msg.Role != "assistant" {
		return nil, fmt.Errorf("response message role must be assistant")
	}
	if len(msg.ToolCalls) > maxToolCalls {
		return nil, fmt.Errorf("too many tool calls in one response (limit %d)", maxToolCalls)
	}
	ids := make(map[string]struct{}, len(msg.ToolCalls))
	for i := range msg.ToolCalls {
		call := &msg.ToolCalls[i]
		if call.ID == "" {
			call.ID = newCallID(i)
		}
		if _, exists := ids[call.ID]; exists {
			return nil, fmt.Errorf("response contains duplicate tool call IDs")
		}
		ids[call.ID] = struct{}{}
		if call.Type == "" {
			call.Type = "function"
		}
		if call.Type != "function" || strings.TrimSpace(call.Function.Name) == "" {
			return nil, fmt.Errorf("response contains invalid tool call")
		}
		var args map[string]json.RawMessage
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args == nil {
			return nil, fmt.Errorf("tool call arguments must be a JSON object")
		}
	}
	return &Response{Message: msg, FinishReason: choice.FinishReason, Usage: parseOpenAIUsage(result.Usage)}, nil
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
