package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkChatStream_TTFT measures the time elapsed from request dispatch
// until the first StreamDelta is received.
func BenchmarkChatStream_TTFT(b *testing.B) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		// Send first chunk immediately
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")
		flusher.Flush()
		// Simulate subsequent chunks
		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\" chunk\"}}]}\n\n")
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer ts.Close()

	client := NewOpenAIClient(Config{BaseURL: ts.URL, APIKey: "test", Model: "gpt-4o"})

	req := Request{
		Messages: []Message{{Role: "user", Content: "hi"}},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var firstDeltaTime time.Duration
		start := time.Now()
		var receivedFirst int32

		_, err := client.ChatStream(context.Background(), req, func(d StreamDelta) {
			if atomic.CompareAndSwapInt32(&receivedFirst, 0, 1) {
				firstDeltaTime = time.Since(start)
			}
		})
		if err != nil {
			b.Fatal(err)
		}
		_ = firstDeltaTime
	}
}

// BenchmarkChatStream_Throughput measures the total streaming throughput for
// responses with 50 tokens.
func BenchmarkChatStream_Throughput(b *testing.B) {
	chunkData := "data: {\"choices\":[{\"delta\":{\"content\":\" token\"}}]}\n\n"
	chunks := strings.Repeat(chunkData, 50) + "data: [DONE]\n\n"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(chunks))
	}))
	defer ts.Close()

	client := NewOpenAIClient(Config{BaseURL: ts.URL, APIKey: "test", Model: "gpt-4o"})
	req := Request{Messages: []Message{{Role: "user", Content: "generate"}}}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := client.ChatStream(context.Background(), req, nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkChatStream_LargeToolCall measures processing and assembly of a large
// tool call chunk (e.g. 64KB file edit).
func BenchmarkChatStream_LargeToolCall(b *testing.B) {
	largeArg := strings.Repeat("A", 32*1024)
	payload := fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"edit_file\",\"arguments\":\"%s\"}}]}}]}\n\ndata: [DONE]\n\n", largeArg)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(payload))
	}))
	defer ts.Close()

	client := NewOpenAIClient(Config{BaseURL: ts.URL, APIKey: "test", Model: "gpt-4o"})
	req := Request{Messages: []Message{{Role: "user", Content: "edit"}}}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp, err := client.ChatStream(context.Background(), req, nil)
		if err != nil {
			b.Fatal(err)
		}
		if len(resp.Message.ToolCalls) != 1 {
			b.Fatalf("expected 1 tool call, got %d", len(resp.Message.ToolCalls))
		}
	}
}

// BenchmarkChatStream_FragmentedToolCalls measures assembling a tool call split
// across 20 small chunks.
func BenchmarkChatStream_FragmentedToolCalls(b *testing.B) {
	var sb strings.Builder
	// First chunk has id and name
	sb.WriteString("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_frag\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\"}}]}}]}\n\n")
	// Intermediate chunks append argument fragments
	for i := 0; i < 18; i++ {
		sb.WriteString("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"sub/\"}}]}}]}\n\n")
	}
	// Final chunk closes json
	sb.WriteString("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"file.go\\\"}\"}}]}}]}\n\n")
	sb.WriteString("data: [DONE]\n\n")
	payload := sb.String()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(payload))
	}))
	defer ts.Close()

	client := NewOpenAIClient(Config{BaseURL: ts.URL, APIKey: "test", Model: "gpt-4o"})
	req := Request{Messages: []Message{{Role: "user", Content: "read"}}}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp, err := client.ChatStream(context.Background(), req, nil)
		if err != nil {
			b.Fatal(err)
		}
		if len(resp.Message.ToolCalls) != 1 {
			b.Fatalf("expected 1 tool call, got %d", len(resp.Message.ToolCalls))
		}
	}
}
