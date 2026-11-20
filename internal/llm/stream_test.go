package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseServer builds a minimal SSE response for use in streaming tests.
func sseServer(t *testing.T, chunks []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
}

func TestChatStreamTextDeltasAssembled(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"content":", world"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
	}
	server := sseServer(t, chunks)
	defer server.Close()

	client := NewOpenAIClient(Config{BaseURL: server.URL, Model: "m", APIKey: "k", Timeout: 5 * time.Second})

	var deltas []string
	resp, err := client.ChatStream(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(d StreamDelta) {
		if d.Content != "" {
			deltas = append(deltas, d.Content)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "Hello, world" {
		t.Fatalf("assembled content = %q", resp.Message.Content)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("finish_reason = %q", resp.FinishReason)
	}
	if resp.Usage.TotalTokens != 7 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if len(deltas) != 2 {
		t.Fatalf("deltas = %v", deltas)
	}
}

func TestChatStreamToolCallAssembled(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.txt\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}
	server := sseServer(t, chunks)
	defer server.Close()

	client := NewOpenAIClient(Config{BaseURL: server.URL, Model: "m", APIKey: "k", Timeout: 5 * time.Second})

	resp, err := client.ChatStream(context.Background(), Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "call_abc" || tc.Function.Name != "read_file" {
		t.Fatalf("tool call = %+v", tc)
	}
	if tc.Function.Arguments != `{"path":"a.txt"}` {
		t.Fatalf("arguments = %q", tc.Function.Arguments)
	}
}

func TestChatStreamNilDeltaCallback(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
	}
	server := sseServer(t, chunks)
	defer server.Close()

	client := NewOpenAIClient(Config{BaseURL: server.URL, Model: "m", APIKey: "k", Timeout: 5 * time.Second})
	resp, err := client.ChatStream(context.Background(), Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "ok" {
		t.Fatalf("content = %q", resp.Message.Content)
	}
}

func TestChatStreamContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hang until the client disconnects.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewOpenAIClient(Config{BaseURL: server.URL, Model: "m", APIKey: "k", Timeout: 5 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.ChatStream(ctx, Request{}, nil)
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestChatStreamHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("overloaded"))
	}))
	defer server.Close()

	client := NewOpenAIClient(Config{BaseURL: server.URL, Model: "m", APIKey: "k", Timeout: 5 * time.Second})
	_, err := client.ChatStream(context.Background(), Request{}, nil)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("expected 503 error, got %v", err)
	}
}

func TestNewOpenAIClientHTTP2Transport(t *testing.T) {
	c := NewOpenAIClient(Config{})
	tr, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport type %T", c.httpClient.Transport)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 not set")
	}
	if tr.MaxIdleConnsPerHost != 4 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 4", tr.MaxIdleConnsPerHost)
	}
	if tr.IdleConnTimeout != 90*time.Second {
		t.Errorf("IdleConnTimeout = %v, want 90s", tr.IdleConnTimeout)
	}
}

// sseServerFromFunc is a helper used to test the scanner with a custom writer.
func TestChatStreamSkipsMalformedChunks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		bw := bufio.NewWriter(w)
		_, _ = bw.WriteString("data: not json\n\n")
		_, _ = bw.WriteString(`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n")
		_, _ = bw.WriteString("data: [DONE]\n\n")
		_ = bw.Flush()
	}))
	defer server.Close()

	client := NewOpenAIClient(Config{BaseURL: server.URL, Model: "m", APIKey: "k", Timeout: 5 * time.Second})
	resp, err := client.ChatStream(context.Background(), Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "ok" {
		t.Fatalf("content = %q (malformed chunk should be skipped)", resp.Message.Content)
	}
}

func TestChatStreamEncodesSteamTrue(t *testing.T) {
	var body map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewOpenAIClient(Config{BaseURL: server.URL, Model: "m", APIKey: "k", Timeout: 5 * time.Second})
	_, err := client.ChatStream(context.Background(), Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	streamVal, ok := body["stream"]
	if !ok {
		t.Fatal("stream field missing from request")
	}
	var streamBool bool
	if err := json.Unmarshal(streamVal, &streamBool); err != nil || !streamBool {
		t.Fatalf("stream = %s, want true", streamVal)
	}
}
