package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIntegrationEndpointTransportPolicy(t *testing.T) {
	for _, tc := range []struct {
		base    string
		allowed bool
	}{
		{"http://127.0.0.1:8080/v1", true}, {"http://[::1]:8080/v1", true}, {"http://localhost:8080/v1", true}, {"https://example.invalid/v1", true},
		{"http://example.invalid/v1", false}, {"http://192.0.2.1/v1", false}, {"ftp://example.invalid/v1", false}, {"https://user:password@example.invalid/v1", false},
	} {
		t.Run(tc.base, func(t *testing.T) {
			c := NewOpenAIClient(Config{BaseURL: tc.base, APIKey: "SYNTHETIC_KEY"})
			called := false
			c.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				called = true
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant"}}]}`))}, nil
			})
			_, err := c.Chat(context.Background(), Request{})
			if tc.allowed && (err != nil || !called) {
				t.Fatalf("permitted endpoint failed: %v", err)
			}
			if !tc.allowed && (err == nil || called) {
				t.Fatalf("unsafe endpoint was requested")
			}
		})
	}
}
func TestIntegrationRedirectDoesNotForwardRequest(t *testing.T) {
	var called atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called.Store(true); w.WriteHeader(200) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	c := NewOpenAIClient(Config{BaseURL: source.URL, APIKey: "SYNTHETIC_KEY"})
	_, err := c.Chat(context.Background(), Request{})
	if err == nil || called.Load() {
		t.Fatal("redirect followed")
	}
}
