package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mrand "math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// GeminiClient talks to the Google Gemini REST API (v1beta).
type GeminiClient struct {
	cfg         Config
	httpClient  *http.Client
	resolver    loopbackResolver
	sleep       func(context.Context, time.Duration) error
	backoffBase time.Duration
}

// NewGeminiClient creates a client configured for the Gemini REST API.
func NewGeminiClient(cfg Config) *GeminiClient {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultGeminiBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = DefaultGeminiModel
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
	c := &GeminiClient{
		cfg:         cfg,
		sleep:       sleepContext,
		backoffBase: 500 * time.Millisecond,
	}
	c.httpClient = newLLMHTTPClient(cfg.BaseURL, cfg.Timeout, func() loopbackResolver { return c.resolver })
	return c
}

// BaseURL returns the configured base URL.
func (c *GeminiClient) BaseURL() string { return c.cfg.BaseURL }

// Model returns the configured model identifier.
func (c *GeminiClient) Model() string { return c.cfg.Model }

// HasKey reports whether an API key is configured.
func (c *GeminiClient) HasKey() bool { return c.cfg.APIKey != "" }

// Provider returns the provider name ("gemini").
func (c *GeminiClient) Provider() string { return ProviderGemini }

// SearchEnabled reports whether Google Search grounding is enabled.
func (c *GeminiClient) SearchEnabled() bool { return c.cfg.GeminiSearch }

// Chat executes a request against Gemini's generateContent endpoint.
func (c *GeminiClient) Chat(ctx context.Context, req Request) (*Response, error) {
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

	modelName := strings.TrimPrefix(c.cfg.Model, "models/")
	endpoint := fmt.Sprintf("%s/models/%s:generateContent", strings.TrimRight(c.cfg.BaseURL, "/"), url.PathEscape(modelName))

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

func (c *GeminiClient) attempt(ctx context.Context, endpoint string, body []byte) (*Response, bool, time.Duration, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("x-goog-api-key", c.cfg.APIKey.SecretValue())
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
		if resp.StatusCode == http.StatusBadRequest && strings.Contains(rawBody, "API_KEY_INVALID") {
			return nil, false, 0, fmt.Errorf("%w (status %d): %s", ErrUnauthorized, resp.StatusCode, clipped)
		}

		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		delay := parseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, retryable, delay, fmt.Errorf("status %d: %s", resp.StatusCode, clipped)
	}

	parsed, err := parseGeminiResponse(data)
	if err != nil {
		return nil, false, 0, err
	}
	return parsed, false, 0, nil
}

func (c *GeminiClient) encode(req Request) ([]byte, error) {
	payload := make(map[string]any)

	// 1. Map tool names for functionResponse matching
	toolNames := make(map[string]string)
	for _, msg := range req.Messages {
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" && tc.Function.Name != "" {
				toolNames[tc.ID] = tc.Function.Name
			}
		}
	}

	// 2. Extract system instructions and build contents
	var systemParts []map[string]any
	var contents []map[string]any

	for _, msg := range req.Messages {
		if msg.Role == "system" {
			if strings.TrimSpace(msg.Content) != "" {
				systemParts = append(systemParts, map[string]any{"text": msg.Content})
			}
			continue
		}
		if msg.Role == "local" || msg.Role == "error" {
			continue
		}

		var role string
		var parts []map[string]any

		switch msg.Role {
		case "user":
			role = "user"
			if msg.Content != "" {
				parts = append(parts, map[string]any{"text": msg.Content})
			}
		case "assistant":
			role = "model"
			if msg.Content != "" {
				parts = append(parts, map[string]any{"text": msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				var argsMap map[string]any
				if tc.Function.Arguments != "" {
					_ = json.Unmarshal([]byte(tc.Function.Arguments), &argsMap)
				}
				if argsMap == nil {
					argsMap = make(map[string]any)
				}
				part := map[string]any{
					"functionCall": map[string]any{
						"name": tc.Function.Name,
						"args": argsMap,
					},
				}
				if tc.ThoughtSignature != "" {
					part["thoughtSignature"] = tc.ThoughtSignature
				}
				parts = append(parts, part)
			}
		case "tool":
			role = "user"
			fnName := toolNames[msg.ToolCallID]
			if fnName == "" {
				fnName = msg.ToolCallID
			}
			var respMap map[string]any
			if strings.HasPrefix(msg.Content, "Error:") || strings.HasPrefix(msg.Content, "error:") {
				respMap = map[string]any{"error": msg.Content}
			} else {
				respMap = map[string]any{"output": msg.Content}
			}
			part := map[string]any{
				"functionResponse": map[string]any{
					"name":     fnName,
					"response": respMap,
				},
			}
			parts = append(parts, part)
		}

		if len(parts) == 0 {
			continue
		}

		// Merge consecutive messages with identical role
		if len(contents) > 0 && contents[len(contents)-1]["role"] == role {
			prevParts := contents[len(contents)-1]["parts"].([]map[string]any)
			contents[len(contents)-1]["parts"] = append(prevParts, parts...)
		} else {
			contents = append(contents, map[string]any{
				"role":  role,
				"parts": parts,
			})
		}
	}

	if len(systemParts) > 0 {
		payload["systemInstruction"] = map[string]any{
			"parts": systemParts,
		}
	}

	if len(contents) > 0 {
		payload["contents"] = contents
	}

	// 3. Tools and Grounding
	var geminiTools []map[string]any
	if len(req.Tools) > 0 {
		var declarations []map[string]any
		for _, t := range req.Tools {
			fn, ok := t["function"].(map[string]any)
			if !ok {
				continue
			}
			decl := map[string]any{
				"name": fn["name"],
			}
			if desc, ok := fn["description"].(string); ok && desc != "" {
				decl["description"] = desc
			}
			if params, ok := fn["parameters"].(map[string]any); ok {
				decl["parameters"] = sanitizeGeminiSchema(params)
			}
			declarations = append(declarations, decl)
		}
		if len(declarations) > 0 {
			geminiTools = append(geminiTools, map[string]any{
				"functionDeclarations": declarations,
			})
		}
	}
	if c.cfg.GeminiSearch {
		geminiTools = append(geminiTools, map[string]any{
			"google_search": map[string]any{},
		})
	}
	if len(geminiTools) > 0 {
		payload["tools"] = geminiTools
	}

	// 4. Generation Config
	payload["generationConfig"] = map[string]any{
		"temperature":     c.cfg.GetTemperature(),
		"maxOutputTokens": c.cfg.MaxTokens,
	}

	return json.Marshal(payload)
}

func sanitizeGeminiSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	out := make(map[string]any, len(schema))
	for k, v := range schema {
		if k == "$schema" || k == "additionalProperties" || k == "title" {
			continue
		}
		if k == "properties" {
			if props, ok := v.(map[string]any); ok {
				cleanedProps := make(map[string]any, len(props))
				for propName, propVal := range props {
					if propMap, ok := propVal.(map[string]any); ok {
						cleanedProps[propName] = sanitizeGeminiSchema(propMap)
					} else {
						cleanedProps[propName] = propVal
					}
				}
				out[k] = cleanedProps
				continue
			}
		}
		if k == "items" {
			if itemMap, ok := v.(map[string]any); ok {
				out[k] = sanitizeGeminiSchema(itemMap)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func parseGeminiResponse(data []byte) (*Response, error) {
	var result struct {
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		Candidates []struct {
			Content struct {
				Role  string `json:"role"`
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall *struct {
						Name string         `json:"name"`
						Args map[string]any `json:"args"`
					} `json:"functionCall"`
					ThoughtSignature string `json:"thoughtSignature"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason      string `json:"finishReason"`
			GroundingMetadata *struct {
				WebSearchQueries []string `json:"webSearchQueries"`
				GroundingChunks  []struct {
					Web struct {
						URI   string `json:"uri"`
						Title string `json:"title"`
					} `json:"web"`
				} `json:"groundingChunks"`
			} `json:"groundingMetadata"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}

	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode gemini response: %w", err)
	}

	if result.PromptFeedback.BlockReason != "" {
		return nil, fmt.Errorf("gemini prompt blocked: %s", result.PromptFeedback.BlockReason)
	}

	if len(result.Candidates) == 0 {
		return nil, fmt.Errorf("gemini response has no candidates")
	}

	candidate := result.Candidates[0]
	switch candidate.FinishReason {
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "MALFORMED_FUNCTION_CALL":
		return nil, fmt.Errorf("gemini response terminated with %s", candidate.FinishReason)
	}

	var textBuilder strings.Builder
	var toolCalls []ToolCall

	for i, part := range candidate.Content.Parts {
		if part.Text != "" {
			textBuilder.WriteString(part.Text)
		}
		if part.FunctionCall != nil {
			if strings.TrimSpace(part.FunctionCall.Name) == "" {
				return nil, fmt.Errorf("gemini response contains invalid tool call")
			}
			if len(toolCalls) >= maxToolCalls {
				return nil, fmt.Errorf("too many tool calls in one response (limit %d)", maxToolCalls)
			}
			argsBytes, err := json.Marshal(part.FunctionCall.Args)
			if err != nil {
				argsBytes = []byte("{}")
			}
			callID := newCallID(i)
			tc := ToolCall{
				ID:               callID,
				Type:             "function",
				ThoughtSignature: part.ThoughtSignature,
			}
			tc.Function.Name = part.FunctionCall.Name
			tc.Function.Arguments = string(argsBytes)
			toolCalls = append(toolCalls, tc)
		}
	}

	// Append Grounding Sources if present
	if candidate.GroundingMetadata != nil && len(candidate.GroundingMetadata.GroundingChunks) > 0 {
		var sources []string
		for _, chunk := range candidate.GroundingMetadata.GroundingChunks {
			uri := chunk.Web.URI
			title := chunk.Web.Title
			if uri != "" && strings.HasPrefix(uri, "https://") {
				if title == "" {
					title = uri
				}
				sources = append(sources, fmt.Sprintf("- [%s](%s)", title, uri))
			}
		}
		if len(sources) > 0 {
			if textBuilder.Len() > 0 {
				textBuilder.WriteString("\n\n")
			}
			textBuilder.WriteString("Sources:\n" + strings.Join(sources, "\n"))
		}
	}

	msg := Message{
		Role:      "assistant",
		Content:   textBuilder.String(),
		ToolCalls: toolCalls,
	}

	usage := Usage{
		PromptTokens:     result.UsageMetadata.PromptTokenCount,
		CompletionTokens: result.UsageMetadata.CandidatesTokenCount,
		TotalTokens:      result.UsageMetadata.TotalTokenCount,
	}

	return &Response{
		Message:      msg,
		FinishReason: candidate.FinishReason,
		Usage:        usage,
	}, nil
}

// CheckModel verifies that cfg.Model exists in GET {base}/models and supports generateContent.
func (c *GeminiClient) CheckModel(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := validateBaseURL(c.cfg.BaseURL); err != nil {
		return err
	}
	ctx = withSchemeDialPolicy(ctx, c.cfg.BaseURL)

	targetNorm := strings.TrimPrefix(c.cfg.Model, "models/")
	nextPageToken := ""
	var names []string

	for page := 0; page < 10; page++ {
		endpoint := strings.TrimRight(c.cfg.BaseURL, "/") + "/models"
		if nextPageToken != "" {
			endpoint += "?pageToken=" + url.QueryEscape(nextPageToken)
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fmt.Errorf("create check model request: %w", err)
		}
		if c.cfg.APIKey != "" {
			httpReq.Header.Set("x-goog-api-key", c.cfg.APIKey.SecretValue())
		}

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			return fmt.Errorf("model check request: %w", err)
		}

		if resp.StatusCode == http.StatusNotFound {
			fmt.Fprintf(os.Stderr, "Warning: model check endpoint %s returned 404; skipping check\n", endpoint)
			_ = resp.Body.Close()
			return nil
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			_ = resp.Body.Close()
			return fmt.Errorf("model check redirected with status %d (redirects are not allowed)", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return fmt.Errorf("model check returned status %d", resp.StatusCode)
		}

		data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read model check response: %w", err)
		}
		if len(data) > maxResponseBytes {
			return fmt.Errorf("model check response too large: exceeds %d bytes", maxResponseBytes)
		}

		var listResp struct {
			Models []struct {
				Name                       string   `json:"name"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(data, &listResp); err != nil {
			return fmt.Errorf("decode models response: %w", err)
		}

		for _, m := range listResp.Models {
			norm := strings.TrimPrefix(m.Name, "models/")
			supportsGen := false
			for _, method := range m.SupportedGenerationMethods {
				if method == "generateContent" {
					supportsGen = true
					break
				}
			}
			if supportsGen {
				if norm == targetNorm {
					return nil
				}
				names = append(names, norm)
			}
		}

		if listResp.NextPageToken == "" {
			break
		}
		nextPageToken = listResp.NextPageToken
	}

	suggestions := closestNames(targetNorm, names, 3)
	if len(suggestions) > 0 {
		return fmt.Errorf("model %q not found; did you mean: %s?", c.cfg.Model, strings.Join(suggestions, ", "))
	}
	return fmt.Errorf("model %q not found on Gemini provider", c.cfg.Model)
}

func (c *GeminiClient) backoff(attempt int) time.Duration {
	base := c.backoffBase * time.Duration(1<<uint(attempt))
	if base <= 0 {
		base = time.Second
	}
	if base > 10*time.Second {
		base = 10 * time.Second
	}
	return time.Duration(mrand.Int63n(int64(base)))
}
