package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amiraq1/nabd/internal/llm"
)

func TestCallLLMReportsTruncatedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"x"}}]}`))
	}))
	defer srv.Close()
	client := llm.NewOpenAIClient(llm.Config{BaseURL: srv.URL, Model: "test", Timeout: 10 * time.Second})
	_, err := client.Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("expected body read error, got %v", err)
	}
}
