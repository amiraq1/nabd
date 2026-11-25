package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGeminiHeaderKeyOnly(t *testing.T) {
	var seenKey string
	var seenQuery string
	var seenURL string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenKey = r.Header.Get("x-goog-api-key")
		seenQuery = r.URL.RawQuery
		seenURL = r.URL.String()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates": [{
				"content": {
					"parts": [{"text": "Hello world!"}],
					"role": "model"
				},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer server.Close()

	client := NewGeminiClient(Config{
		Provider: "gemini",
		BaseURL:  server.URL,
		Model:    "gemini-2.5-flash",
		APIKey:   "secret-gemini-key-12345",
	})

	resp, err := client.Chat(context.Background(), Request{
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if resp.Message.Content != "Hello world!" {
		t.Fatalf("unexpected content: %q", resp.Message.Content)
	}

	// Verify key arrived strictly via header
	if seenKey != "secret-gemini-key-12345" {
		t.Fatalf("expected x-goog-api-key 'secret-gemini-key-12345', got %q", seenKey)
	}
	// Verify key NEVER appeared in URL query or path
	if strings.Contains(seenQuery, "key=") || strings.Contains(seenURL, "secret-gemini-key-12345") {
		t.Fatalf("API key leaked in URL: %s", seenURL)
	}
}

func TestGeminiMultiTurnAndParallelToolMerging(t *testing.T) {
	var decodedPayload struct {
		SystemInstruction struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"systemInstruction"`
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text         string `json:"text"`
				FunctionCall *struct {
					Name string         `json:"name"`
					Args map[string]any `json:"args"`
				} `json:"functionCall"`
				FunctionResponse *struct {
					Name     string         `json:"name"`
					Response map[string]any `json:"response"`
				} `json:"functionResponse"`
				ThoughtSignature string `json:"thoughtSignature"`
			} `json:"parts"`
		} `json:"contents"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &decodedPayload); err != nil {
			t.Errorf("unmarshal payload: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates": [{
				"content": {
					"parts": [{"text": "Tools executed successfully"}],
					"role": "model"
				},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer server.Close()

	client := NewGeminiClient(Config{
		Provider: "gemini",
		BaseURL:  server.URL,
		Model:    "gemini-2.5-flash",
		APIKey:   "gemini-test-key",
	})

	// Scenario:
	// 1. System prompt
	// 2. User prompt 1
	// 3. User prompt 2 (consecutive user messages -> must be merged)
	// 4. Assistant tool calls (with thoughtSignature)
	// 5. Tool result 1
	// 6. Tool result 2 (parallel tool results -> consecutive tool messages -> must be merged into ONE user role content)
	req := Request{
		Messages: []Message{
			{Role: "system", Content: "You are NabdCoder"},
			{Role: "user", Content: "Read these two files"},
			{Role: "user", Content: "Here is extra detail"},
			{
				Role:    "assistant",
				Content: "Checking files now",
				ToolCalls: []ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "read_file",
							Arguments: `{"path":"a.go"}`,
						},
						ThoughtSignature: "sig_abc_123",
					},
					{
						ID:   "call_2",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "read_file",
							Arguments: `{"path":"b.go"}`,
						},
						ThoughtSignature: "sig_xyz_456",
					},
				},
			},
			{Role: "tool", ToolCallID: "call_1", Content: "package a"},
			{Role: "tool", ToolCallID: "call_2", Content: "package b"},
		},
	}

	_, err := client.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}

	// 1. Check system instruction
	if len(decodedPayload.SystemInstruction.Parts) != 1 || decodedPayload.SystemInstruction.Parts[0].Text != "You are NabdCoder" {
		t.Fatalf("unexpected systemInstruction: %+v", decodedPayload.SystemInstruction)
	}

	// 2. Check contents:
	// Contents should have 3 items:
	// Item 0: user (merged text 1 and text 2)
	// Item 1: model (text + 2 functionCalls with thoughtSignatures)
	// Item 2: user (merged 2 functionResponses matching tool names)
	if len(decodedPayload.Contents) != 3 {
		t.Fatalf("expected 3 contents items, got %d", len(decodedPayload.Contents))
	}

	// Item 0: user
	c0 := decodedPayload.Contents[0]
	if c0.Role != "user" || len(c0.Parts) != 2 {
		t.Fatalf("expected merged user content with 2 parts, got: %+v", c0)
	}

	// Item 1: model
	c1 := decodedPayload.Contents[1]
	if c1.Role != "model" || len(c1.Parts) != 3 { // 1 text + 2 function calls
		t.Fatalf("expected model content with 3 parts, got: %+v", c1)
	}
	if c1.Parts[1].FunctionCall.Name != "read_file" || c1.Parts[1].ThoughtSignature != "sig_abc_123" {
		t.Fatalf("expected thoughtSignature 'sig_abc_123' on call 1, got %+v", c1.Parts[1])
	}
	if c1.Parts[2].FunctionCall.Name != "read_file" || c1.Parts[2].ThoughtSignature != "sig_xyz_456" {
		t.Fatalf("expected thoughtSignature 'sig_xyz_456' on call 2, got %+v", c1.Parts[2])
	}

	// Item 2: user (consecutive tool results merged into single user role content)
	c2 := decodedPayload.Contents[2]
	if c2.Role != "user" {
		t.Fatalf("expected user role for tool responses, got %q", c2.Role)
	}
	if len(c2.Parts) != 2 {
		t.Fatalf("expected 2 functionResponse parts merged into single user content, got %d", len(c2.Parts))
	}
	if c2.Parts[0].FunctionResponse == nil || c2.Parts[0].FunctionResponse.Name != "read_file" {
		t.Fatalf("expected functionResponse name 'read_file', got: %+v", c2.Parts[0])
	}
	if c2.Parts[0].FunctionResponse.Response["output"] != "package a" {
		t.Fatalf("expected output 'package a', got: %+v", c2.Parts[0].FunctionResponse.Response)
	}
	if c2.Parts[1].FunctionResponse == nil || c2.Parts[1].FunctionResponse.Name != "read_file" {
		t.Fatalf("expected functionResponse name 'read_file', got: %+v", c2.Parts[1])
	}
	if c2.Parts[1].FunctionResponse.Response["output"] != "package b" {
		t.Fatalf("expected output 'package b', got: %+v", c2.Parts[1].FunctionResponse.Response)
	}
}

func TestGeminiSchemaSanitization(t *testing.T) {
	rawSchema := map[string]any{
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"title":                "ReadFileInput",
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "path to file",
				"title":       "PathField",
			},
			"options": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"verbose": map[string]any{"type": "boolean"},
				},
			},
		},
		"required": []any{"path"},
	}

	cleaned := sanitizeGeminiSchema(rawSchema)

	if _, exists := cleaned["$schema"]; exists {
		t.Error("$schema was not removed")
	}
	if _, exists := cleaned["title"]; exists {
		t.Error("title was not removed")
	}
	if _, exists := cleaned["additionalProperties"]; exists {
		t.Error("additionalProperties was not removed")
	}

	props := cleaned["properties"].(map[string]any)
	pathProp := props["path"].(map[string]any)
	if _, exists := pathProp["title"]; exists {
		t.Error("nested title was not removed")
	}

	optProp := props["options"].(map[string]any)
	if _, exists := optProp["additionalProperties"]; exists {
		t.Error("nested additionalProperties was not removed")
	}
}

func TestGeminiSearchGrounding(t *testing.T) {
	var capturedTools []map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody struct {
			Tools []map[string]any `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		capturedTools = reqBody.Tools

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates": [{
				"content": {
					"parts": [{"text": "Go 1.26 was released with new features."}],
					"role": "model"
				},
				"finishReason": "STOP",
				"groundingMetadata": {
					"webSearchQueries": ["Go 1.26 release notes"],
					"groundingChunks": [
						{
							"web": {
								"uri": "https://go.dev/doc/go1.26",
								"title": "Go 1.26 Release Notes"
							}
						}
					]
				}
			}]
		}`))
	}))
	defer server.Close()

	// 1. With GeminiSearch enabled
	client := NewGeminiClient(Config{
		Provider:     "gemini",
		BaseURL:      server.URL,
		Model:        "gemini-2.5-flash",
		APIKey:       "key",
		GeminiSearch: true,
	})

	resp, err := client.Chat(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "What's new in Go 1.26?"}},
	})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}

	// Verify google_search tool was added
	foundSearch := false
	for _, tool := range capturedTools {
		if _, exists := tool["google_search"]; exists {
			foundSearch = true
			break
		}
	}
	if !foundSearch {
		t.Fatal("expected google_search in tools when GeminiSearch=true")
	}

	// Verify grounding sources appended to content
	if !strings.Contains(resp.Message.Content, "Sources:") || !strings.Contains(resp.Message.Content, "https://go.dev/doc/go1.26") {
		t.Fatalf("expected grounding sources in response content, got: %s", resp.Message.Content)
	}
}

func TestGeminiTemperatureZeroPreserved(t *testing.T) {
	var capturedConfig struct {
		GenerationConfig struct {
			Temperature *float64 `json:"temperature"`
		} `json:"generationConfig"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedConfig)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates": [{"content": {"parts": [{"text": "ok"}], "role": "model"}, "finishReason": "STOP"}]
		}`))
	}))
	defer server.Close()

	zero := 0.0
	client := NewGeminiClient(Config{
		Provider:    "gemini",
		BaseURL:     server.URL,
		Model:       "gemini-2.5-flash",
		APIKey:      "key",
		Temperature: &zero,
	})

	_, err := client.Chat(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}

	if capturedConfig.GenerationConfig.Temperature == nil || *capturedConfig.GenerationConfig.Temperature != 0.0 {
		t.Fatalf("expected explicit temperature 0.0, got %v", capturedConfig.GenerationConfig.Temperature)
	}
}

func TestGeminiErrorHandlingAndRetries(t *testing.T) {
	attempts := 0
	apiKey := "secret-super-key-12345"

	// 1. Test 401 Unauthorized -> ErrUnauthorized immediately, no retry
	server401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("Unauthorized key " + apiKey))
	}))
	defer server401.Close()

	client := NewGeminiClient(Config{
		Provider: "gemini",
		BaseURL:  server401.URL,
		Model:    "gemini-2.5-flash",
		APIKey:   Secret(apiKey),
	})

	attempts = 0
	_, err := client.Chat(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected exactly 1 attempt on 401, got %d", attempts)
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatal("API key was not redacted from error message")
	}

	// 2. Test 400 with API_KEY_INVALID -> ErrUnauthorized immediately, no retry
	server400APIKey := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "API_KEY_INVALID: Key is invalid"}}`))
	}))
	defer server400APIKey.Close()

	client.cfg.BaseURL = server400APIKey.URL
	attempts = 0
	_, err = client.Chat(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized for API_KEY_INVALID, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected exactly 1 attempt on API_KEY_INVALID, got %d", attempts)
	}

	// 3. Test 429 Retryable -> retries up to maxAttempts
	server429 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error": "Resource exhausted"}`))
	}))
	defer server429.Close()

	client.cfg.BaseURL = server429.URL
	client.backoffBase = time.Millisecond // fast backoff for test
	attempts = 0
	_, err = client.Chat(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "after 4 attempts") {
		t.Fatalf("expected retry exhaustion after 4 attempts, got %v", err)
	}
	if attempts != 4 {
		t.Fatalf("expected 4 attempts for 429, got %d", attempts)
	}

	// 4. Test error response larger than 64KiB is scrubbed and key redacted
	largeBody := strings.Repeat("A", 70000) + apiKey
	serverLargeErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(largeBody))
	}))
	defer serverLargeErr.Close()

	client.cfg.BaseURL = serverLargeErr.URL
	_, err = client.Chat(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error on 400 large body")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatal("secret leaked in large error response")
	}
}

func TestGeminiSafetyAndBlockReasons(t *testing.T) {
	// 1. promptFeedback.blockReason
	serverBlocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"promptFeedback": {"blockReason": "SAFETY"}
		}`))
	}))
	defer serverBlocked.Close()

	client := NewGeminiClient(Config{
		Provider: "gemini",
		BaseURL:  serverBlocked.URL,
		Model:    "gemini-2.5-flash",
		APIKey:   "key",
	})

	_, err := client.Chat(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "gemini prompt blocked: SAFETY") {
		t.Fatalf("expected prompt blocked error, got %v", err)
	}

	// 2. candidate finishReason SAFETY
	serverSafety := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates": [{
				"content": {"parts": [{"text": "partial"}], "role": "model"},
				"finishReason": "SAFETY"
			}]
		}`))
	}))
	defer serverSafety.Close()

	client.cfg.BaseURL = serverSafety.URL
	_, err = client.Chat(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "gemini response terminated with SAFETY") {
		t.Fatalf("expected finishReason error, got %v", err)
	}

	// 3. empty candidates
	serverEmpty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates": []}`))
	}))
	defer serverEmpty.Close()

	client.cfg.BaseURL = serverEmpty.URL
	_, err = client.Chat(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "no candidates") {
		t.Fatalf("expected no candidates error, got %v", err)
	}
}

func TestGeminiCheckModel(t *testing.T) {
	pageRequests := 0
	var capturedKey string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedKey = r.Header.Get("x-goog-api-key")
		pageRequests++

		if r.URL.Query().Get("pageToken") == "page2" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"models": [
					{
						"name": "models/gemini-2.5-flash",
						"supportedGenerationMethods": ["generateContent"]
					}
				]
			}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"models": [
				{
					"name": "models/text-embedding-004",
					"supportedGenerationMethods": ["embedContent"]
				},
				{
					"name": "models/gemini-2.5-pro",
					"supportedGenerationMethods": ["generateContent"]
				}
			],
			"nextPageToken": "page2"
		}`))
	}))
	defer server.Close()

	// 1. Success matching target model normalized across pagination
	client := NewGeminiClient(Config{
		Provider: "gemini",
		BaseURL:  server.URL,
		Model:    "gemini-2.5-flash", // without models/ prefix
		APIKey:   "gemini-check-key",
	})

	err := client.CheckModel(context.Background())
	if err != nil {
		t.Fatalf("CheckModel failed: %v", err)
	}
	if capturedKey != "gemini-check-key" {
		t.Fatalf("expected x-goog-api-key 'gemini-check-key', got %q", capturedKey)
	}
	if pageRequests != 2 {
		t.Fatalf("expected 2 page requests, got %d", pageRequests)
	}

	// 2. Success matching target model with models/ prefix configured
	client.cfg.Model = "models/gemini-2.5-pro"
	err = client.CheckModel(context.Background())
	if err != nil {
		t.Fatalf("CheckModel with models/ prefix failed: %v", err)
	}

	// 3. Suggestions on model not found
	client.cfg.Model = "gemini-2.5-flsh" // typo
	err = client.CheckModel(context.Background())
	if err == nil || !strings.Contains(err.Error(), "did you mean: gemini-2.5-flash") {
		t.Fatalf("expected suggestion for typo, got %v", err)
	}

	// 4. Redirects rejected
	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/somewhere-else", http.StatusFound)
	}))
	defer redirectServer.Close()

	client.cfg.BaseURL = redirectServer.URL
	err = client.CheckModel(context.Background())
	if err == nil || !strings.Contains(err.Error(), "redirects are not allowed") {
		t.Fatalf("expected redirect error, got %v", err)
	}
}

func TestGeminiContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewGeminiClient(Config{
		Provider: "gemini",
		BaseURL:  server.URL,
		Model:    "gemini-2.5-flash",
		APIKey:   "key",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.Chat(ctx, Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context error, got %v", err)
	}
}
