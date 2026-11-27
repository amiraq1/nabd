package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

var webEnvKeys = []string{
	WebSearchProviderEnv, WebBraveAPIKeyEnv,
	WebTavilyAPIKeyEnv, WebSearXNGURLEnv,
	WebFirecrawlAPIKeyEnv, WebFirecrawlAPIKeyFileEnv, WebFirecrawlBaseURLEnv,
}

func newWebIdentityRegistry(t *testing.T, env map[string]string) *Registry {
	t.Helper()
	for _, key := range webEnvKeys {
		t.Setenv(key, "")
	}
	for key, value := range env {
		t.Setenv(key, value)
	}
	reg, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

func searchRegistry(t *testing.T) *Registry {
	return newWebIdentityRegistry(t, map[string]string{
		WebSearchProviderEnv: "searxng",
		WebSearXNGURLEnv:     "https://search.example.com",
	})
}

func TestCanonicalWebSearchIdentityIsStableAndFieldBound(t *testing.T) {
	reg := searchRegistry(t)

	query, maxResults, id, err := reg.CanonicalWebSearch(json.RawMessage(`{"query":"alpha beta","max_results":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if query != "alpha beta" || maxResults != 3 {
		t.Fatalf("canonical values = %q/%d", query, maxResults)
	}
	if !strings.HasPrefix(id, "websearch:sha256:") {
		t.Fatalf("identity prefix = %q", id)
	}

	// Re-ordered but semantically identical JSON must keep the same identity.
	_, _, idReordered, err := reg.CanonicalWebSearch(json.RawMessage(`{ "max_results": 3, "query": "alpha beta" }`))
	if err != nil {
		t.Fatal(err)
	}
	if idReordered != id {
		t.Fatalf("identity changed on JSON reordering: %q != %q", idReordered, id)
	}

	// Every field that affects the outbound request must change the identity.
	for _, raw := range []string{
		`{"query":"alpha beta","max_results":4}`,
		`{"query":"alpha gamma","max_results":3}`,
		`{"query":"alpha  beta","max_results":3}`,
	} {
		_, _, other, err := reg.CanonicalWebSearch(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if other == id {
			t.Fatalf("identity did not change for %s", raw)
		}
	}

	// Provider and destination are part of the identity too.
	brave := newWebIdentityRegistry(t, map[string]string{
		WebSearchProviderEnv: "brave",
		WebBraveAPIKeyEnv:    "brave-key",
	})
	_, _, braveID, err := brave.CanonicalWebSearch(json.RawMessage(`{"query":"alpha beta","max_results":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if braveID == id {
		t.Fatal("identity is not bound to the provider/destination")
	}

	if _, _, _, err := reg.CanonicalWebSearch(json.RawMessage(`{"query":"","max_results":3}`)); err == nil {
		t.Fatal("empty query accepted")
	}
}

func TestWebToolsRequireManualApprovalInEveryMode(t *testing.T) {
	reg := searchRegistry(t)
	searchArgs := json.RawMessage(`{"query":"manual only","max_results":2}`)
	fetchArgs := json.RawMessage(`{"url":"https://example.com/guide"}`)
	_, _, searchID, err := reg.CanonicalWebSearch(searchArgs)
	if err != nil {
		t.Fatal(err)
	}
	_, _, fetchID, err := CanonicalWebFetchURL(fetchArgs)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"web_search", "web_fetch"} {
		if !reg.RequiresManualApproval(name) {
			t.Fatalf("%s must require manual approval", name)
		}
	}

	if _, err := reg.ApproveAuto("web_search", searchArgs, autoApproveAllTag); err == nil {
		t.Fatal("web_search accepted auto-approval")
	}
	if _, err := reg.ApproveWithIdentityAuto("web_search", searchArgs, searchID, autoApproveAllTag); err == nil {
		t.Fatal("web_search accepted identity-bound auto-approval")
	}
	if _, err := reg.ApproveAuto("web_fetch", fetchArgs, autoApproveAllTag); err == nil {
		t.Fatal("web_fetch accepted auto-approval")
	}
	if _, err := reg.ApproveWithIdentityAuto("web_fetch", fetchArgs, fetchID, autoApproveAllTag); err == nil {
		t.Fatal("web_fetch accepted identity-bound auto-approval")
	}

	// The manual paths stay available.
	if _, err := reg.Approve("web_search", searchArgs); err != nil {
		t.Fatalf("manual web_search approval refused: %v", err)
	}
	if _, err := reg.ApproveWithIdentity("web_fetch", fetchArgs, fetchID); err != nil {
		t.Fatalf("manual web_fetch approval refused: %v", err)
	}
}

func TestWebSearchApprovalTokenIsBoundToEveryField(t *testing.T) {
	t.Setenv(WebSearchProviderEnv, "searxng")
	t.Setenv(WebSearXNGURLEnv, "https://search.example.com")
	reg, _ := newWebTestRegistry(t, map[string]string{
		WebSearchProviderEnv: "searxng",
		WebSearXNGURLEnv:     "https://search.example.com",
	}, nil)

	args1 := json.RawMessage(`{"query":"original query","max_results":2}`)
	args2 := json.RawMessage(`{"query":"swapped query","max_results":2}`)
	args3 := json.RawMessage(`{"query":"original query","max_results":3}`)

	app, err := reg.Approve("web_search", args1)
	if err != nil {
		t.Fatal(err)
	}
	for _, swapped := range []json.RawMessage{args2, args3} {
		out := reg.RunApproved(context.Background(), "web_search", swapped, app)
		if !strings.Contains(out, "invalid or expired") {
			t.Fatalf("token bound to args1 was accepted for %s: %s", swapped, out)
		}
	}

	// A mismatched identity is refused before a token exists.
	if _, _, id, err := reg.CanonicalWebSearch(args1); err == nil {
		if _, err := reg.ApproveWithIdentity("web_search", args1, id+"-wrong"); err == nil {
			t.Fatal("web_search accepted a mismatched canonical identity")
		}
	}
}
