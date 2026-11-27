package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/amiraq1/nabd/internal/tools"
)

func webSearchTestRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	for _, key := range []string{
		tools.WebSearchProviderEnv, tools.WebBraveAPIKeyEnv,
		tools.WebTavilyAPIKeyEnv, tools.WebSearXNGURLEnv,
		tools.WebFirecrawlAPIKeyEnv, tools.WebFirecrawlAPIKeyFileEnv, tools.WebFirecrawlBaseURLEnv,
	} {
		t.Setenv(key, "")
	}
	t.Setenv(tools.WebSearchProviderEnv, "searxng")
	t.Setenv(tools.WebSearXNGURLEnv, "https://search.example.com")
	reg, err := tools.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

func webSearchToolCall(args string) ToolCall {
	return ToolCall{
		ID: "call-web-1",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "web_search", Arguments: args},
	}
}

func TestWebSearchApprovalPreviewShowsCompleteCanonicalRequest(t *testing.T) {
	reg := webSearchTestRegistry(t)
	args := `{"query":"مرحبا عالم alpha","max_results":3}`

	preview, err := prepareApprovalPreview(reg, webSearchToolCall(args))
	if err != nil {
		t.Fatalf("prepareApprovalPreview: %v", err)
	}
	query, maxResults, identity, err := reg.CanonicalWebSearch(json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	if preview.IdentitySHA256 != identity {
		t.Fatalf("preview identity %q != canonical identity %q", preview.IdentitySHA256, identity)
	}
	if !strings.Contains(preview.Summary, query) {
		t.Fatalf("preview does not show the full query: %s", preview.Summary)
	}
	if !strings.Contains(preview.Summary, "max_results: 3") || !strings.Contains(preview.Summary, "search.example.com") {
		t.Fatalf("preview is missing destination or option fields: %s", preview.Summary)
	}
	if !strings.Contains(preview.Summary, "outbound network request: web search via searxng") {
		t.Fatalf("preview is missing the provider: %s", preview.Summary)
	}
	if maxResults != 3 {
		t.Fatalf("max_results = %d", maxResults)
	}

	// The approval token must be bound to the same identity the preview showed.
	app, err := reg.ApproveWithIdentity("web_search", json.RawMessage(args), preview.IdentitySHA256)
	if err != nil {
		t.Fatalf("manual approval with preview identity: %v", err)
	}
	if app.IdentitySHA256 != identity {
		t.Fatalf("token identity %q != preview %q", app.IdentitySHA256, identity)
	}

	// Swapping max_results after the preview invalidates the token.
	swapped := json.RawMessage(`{"query":"مرحبا عالم alpha","max_results":4}`)
	out := reg.RunApproved(context.Background(), "web_search", swapped, app)
	if !strings.Contains(out, "invalid or expired") {
		t.Fatalf("token accepted a swapped max_results: %s", out)
	}
}

func TestWebSearchQueryRejectsInvisibleAndControlCharacters(t *testing.T) {
	reg := webSearchTestRegistry(t)
	for _, args := range []string{
		`{"query":"a\u200bb"}`,
		`{"query":"a\u202eb"}`,
		`{"query":"a\u2066b"}`,
		`{"query":"a\u2060b"}`,
		"{\"query\":\"a\u0085b\"}",
		"{\"query\":\"a\u0000b\"}",
		"{\"query\":\"a\u001bb\"}",
	} {
		if _, err := prepareApprovalPreview(reg, webSearchToolCall(args)); err == nil {
			t.Fatalf("invisible/control query was accepted: %q", args)
		}
	}
}

func TestWebSearchLongUnicodeQueryIsShownFullyWithoutClipping(t *testing.T) {
	reg := webSearchTestRegistry(t)
	query := strings.Repeat("ا", 399) + "x"
	if utf8.RuneCountInString(query) != 400 {
		t.Fatalf("test query has %d runes", utf8.RuneCountInString(query))
	}
	raw, err := json.Marshal(map[string]any{"query": query, "max_results": 5})
	if err != nil {
		t.Fatal(err)
	}

	preview, err := prepareApprovalPreview(reg, webSearchToolCall(string(raw)))
	if err != nil {
		t.Fatalf("prepareApprovalPreview: %v", err)
	}

	// Reconstruct the query from the displayed lines: every rune must be
	// present, with no ellipsis or silent clipping.
	var rebuilt strings.Builder
	lines := strings.Split(preview.Summary, "\n")
	wrapped := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "query: ") {
			rebuilt.WriteString(strings.TrimPrefix(line, "query: "))
			wrapped++
		}
	}
	if rebuilt.String() != query {
		t.Fatalf("preview did not contain the complete query (%d/%d runes)", utf8.RuneCountInString(rebuilt.String()), utf8.RuneCountInString(query))
	}
	if wrapped == 0 || wrapped > webSearchApprovalMaxLines+1 {
		t.Fatalf("unexpected wrapped line count: %d", wrapped)
	}
	if strings.Contains(preview.Summary, "...") {
		t.Fatal("preview used an ellipsis instead of showing the full query")
	}

	// Rendering must not drop content; the terminal viewport itself is not
	// simulated here.
	if rendered := renderApprovalSummary(preview.Summary, 40); !strings.Contains(rendered, "query: ") {
		t.Fatal("rendered approval summary lost the query block")
	}
}

func TestWebSearchAutoApprovalEntryPointsRefuseWebTools(t *testing.T) {
	reg := webSearchTestRegistry(t)
	searchArgs := json.RawMessage(`{"query":"auto refused"}`)
	fetchArgs := json.RawMessage(`{"url":"https://example.com/page"}`)
	_, _, searchID, err := reg.CanonicalWebSearch(searchArgs)
	if err != nil {
		t.Fatal(err)
	}
	_, _, fetchID, err := tools.CanonicalWebFetchURL(fetchArgs)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := reg.ApproveAuto("web_search", searchArgs, "all"); err == nil {
		t.Fatal("ApproveAuto accepted web_search")
	}
	if _, err := reg.ApproveWithIdentityAuto("web_search", searchArgs, searchID, "all"); err == nil {
		t.Fatal("ApproveWithIdentityAuto accepted web_search")
	}
	if _, err := reg.ApproveAuto("web_fetch", fetchArgs, "all"); err == nil {
		t.Fatal("ApproveAuto accepted web_fetch")
	}
	if _, err := reg.ApproveWithIdentityAuto("web_fetch", fetchArgs, fetchID, "all"); err == nil {
		t.Fatal("ApproveWithIdentityAuto accepted web_fetch")
	}
	if _, err := reg.ApprovePreviewAuto("web_fetch", fetchArgs, tools.ApprovalState{}, "all"); err == nil {
		t.Fatal("ApprovePreviewAuto accepted web_fetch")
	}

	m := initialModel(reg, &mockClient{}, AutoApproveAll)
	m.pendingTools = []ToolCall{webSearchToolCall(`{"query":"auto refused"}`)}
	next, cmd := m.advance()
	ui := next.(uiModel)
	if !ui.awaitingApproval || cmd != nil {
		t.Fatal("AutoApproveAll did not fall back to a manual decision for web_search")
	}
}
