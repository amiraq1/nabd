package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amiraq1/nabd/internal/tools"
)

func TestCallLLMReportsTruncatedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"x"}}]}`))
	}))
	defer srv.Close()

	old := baseURL
	baseURL = srv.URL
	defer func() { baseURL = old }()

	reg, err := tools.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = callLLM(reg, []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected a read error for a truncated response body")
	}
	if !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("expected unexpected EOF, got %v", err)
	}
}
