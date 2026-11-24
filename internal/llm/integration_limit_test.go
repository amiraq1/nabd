package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIntegrationLLMResponseLimitBoundary(t *testing.T) {
	valid := `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprintf("extra_%d", extra), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(valid + strings.Repeat(" ", maxResponseBytes-len(valid)+extra)))
			}))
			defer server.Close()
			client := NewOpenAIClient(Config{BaseURL: server.URL})
			_, err := client.Chat(context.Background(), Request{})
			if extra == 0 && err != nil {
				t.Fatalf("exact limit rejected: %v", err)
			}
			if extra == 1 && (err == nil || !strings.Contains(err.Error(), "response too large")) {
				t.Fatalf("over-limit response not rejected clearly: %v", err)
			}
		})
	}
}
