package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	WebSearchProviderEnv   = "NABD_WEB_SEARCH"
	WebBraveAPIKeyEnv      = "NABD_BRAVE_API_KEY"
	WebTavilyAPIKeyEnv     = "NABD_TAVILY_API_KEY"
	WebSearXNGURLEnv       = "NABD_SEARXNG_URL"
	WebFirecrawlAPIKeyEnv  = "NABD_FIRECRAWL_API_KEY"
	WebFirecrawlBaseURLEnv = "NABD_FIRECRAWL_BASE_URL"

	defaultBraveEndpoint    = "https://api.search.brave.com/res/v1/web/search"
	defaultTavilyEndpoint   = "https://api.tavily.com/search"
	defaultFirecrawlBaseURL = "https://api.firecrawl.dev"

	maxSearchQueryRunes     = 400
	maxSearchQueryBytes     = 1024
	defaultSearchMaxResults = 5
	minSearchMaxResults     = 1
	maxSearchMaxResults     = 10
	maxProviderErrorSnippet = 180
)

type webSecret string

func (s webSecret) String() string   { return "[REDACTED]" }
func (s webSecret) GoString() string { return "[REDACTED]" }
func (s webSecret) raw() string      { return string(s) }

// WebConfig holds validated configuration for web_search and web_fetch tools.
type WebConfig struct {
	Provider string
	Endpoint string
	APIKey   webSecret
	netHooks *webNetHooks // nil in production; injected solely by tests
}

// LoadWebConfigFromEnv validates NABD_WEB_SEARCH and provider settings.
// When NABD_WEB_SEARCH is unset, empty, or "off", it returns (nil, nil), leaving
// web tools disabled by default. Unknown providers or missing credentials fail closed.
func LoadWebConfigFromEnv() (*WebConfig, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(WebSearchProviderEnv)))
	if raw == "" || raw == "off" {
		return nil, nil
	}

	switch raw {
	case "brave":
		key := strings.TrimSpace(os.Getenv(WebBraveAPIKeyEnv))
		if key == "" {
			return nil, fmt.Errorf("%s=brave requires %s to be set", WebSearchProviderEnv, WebBraveAPIKeyEnv)
		}
		return &WebConfig{
			Provider: "brave",
			Endpoint: defaultBraveEndpoint,
			APIKey:   webSecret(key),
		}, nil

	case "tavily":
		key := strings.TrimSpace(os.Getenv(WebTavilyAPIKeyEnv))
		if key == "" {
			return nil, fmt.Errorf("%s=tavily requires %s to be set", WebSearchProviderEnv, WebTavilyAPIKeyEnv)
		}
		return &WebConfig{
			Provider: "tavily",
			Endpoint: defaultTavilyEndpoint,
			APIKey:   webSecret(key),
		}, nil

	case "searxng":
		baseURL := strings.TrimSpace(os.Getenv(WebSearXNGURLEnv))
		if baseURL == "" {
			return nil, fmt.Errorf("%s=searxng requires %s to be set", WebSearchProviderEnv, WebSearXNGURLEnv)
		}
		u, err := validateExternalURL(baseURL)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", WebSearXNGURLEnv, sanitizeNetworkError(err))
		}
		if u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("invalid %s: URL must not include query parameters or fragments", WebSearXNGURLEnv)
		}
		cleanBase := strings.TrimRight(u.String(), "/")
		return &WebConfig{
			Provider: "searxng",
			Endpoint: cleanBase,
		}, nil

	case "firecrawl":
		key := strings.TrimSpace(os.Getenv(WebFirecrawlAPIKeyEnv))
		if key == "" {
			return nil, fmt.Errorf("%s=firecrawl requires %s to be set", WebSearchProviderEnv, WebFirecrawlAPIKeyEnv)
		}
		baseURL := strings.TrimSpace(os.Getenv(WebFirecrawlBaseURLEnv))
		if baseURL == "" {
			baseURL = defaultFirecrawlBaseURL
		} else {
			u, err := validateExternalURL(baseURL)
			if err != nil {
				return nil, fmt.Errorf("invalid %s: %w", WebFirecrawlBaseURLEnv, sanitizeNetworkError(err, key))
			}
			if u.RawQuery != "" || u.Fragment != "" {
				return nil, fmt.Errorf("invalid %s: URL must not include query parameters or fragments", WebFirecrawlBaseURLEnv)
			}
			baseURL = strings.TrimRight(u.String(), "/")
		}
		return &WebConfig{
			Provider: "firecrawl",
			Endpoint: baseURL,
			APIKey:   webSecret(key),
		}, nil

	default:
		return nil, fmt.Errorf("invalid %s value %q: must be off, brave, tavily, searxng, or firecrawl", WebSearchProviderEnv, raw)
	}
}

// WebSearchProvider returns the active search provider name or "disabled".
func (r *Registry) WebSearchProvider() string {
	if r == nil || r.webConfig == nil || !r.Has("web_search") {
		return "disabled"
	}
	return r.webConfig.Provider
}

// IsWebSearchEnabled reports whether web_search and web_fetch are configured and registered.
func (r *Registry) IsWebSearchEnabled() bool {
	return r.WebSearchProvider() != "disabled"
}

func (r *Registry) registerWebTools() {
	if r.webConfig == nil {
		return
	}

	r.handlers["web_search"] = ToolSpec{
		Name:           "web_search",
		Description:    fmt.Sprintf("Search public web sources using the configured search provider (%s). Returns bounded untrusted titles, URLs, and snippets. Never put secrets or API keys in queries.", r.webConfig.Provider),
		ReadOnly:       false, // Outbound network egress always requires approval (never bypassed in readonly mode)
		MaxOutputBytes: webMaxOutputBytes,
		Parameters:     json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Public search query (max 400 Unicode chars / 1024 bytes). Never include secrets."},"max_results":{"type":"integer","minimum":1,"maximum":10,"description":"Maximum results to return (1-10, default 5)"}},"required":["query"]}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			return r.executeWebSearch(ctx, args)
		},
	}

	fetchDesc := "Fetch and extract bounded text from a public https:// URL. Private/loopback destinations, userinfo URLs, non-HTTPS schemes, and binary files are blocked. Output is capped at 8 KiB and wrapped as untrusted web content."
	if r.webConfig.Provider == "firecrawl" {
		fetchDesc = "Scrape public https:// URL via Firecrawl as bounded Markdown after SSRF validation of the target URL. Private/loopback destinations, userinfo URLs, and non-HTTPS schemes are blocked. Output is capped at 8 KiB and wrapped as untrusted web content."
	}

	r.handlers["web_fetch"] = ToolSpec{
		Name:           "web_fetch",
		Description:    fetchDesc,
		ReadOnly:       false, // Outbound network egress always requires approval (never bypassed in readonly mode)
		MaxOutputBytes: webMaxOutputBytes,
		Parameters:     json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"Public https:// URL to fetch (max 2048 chars)."}},"required":["url"]}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			return r.executeWebFetch(ctx, args)
		},
	}
}

type webSearchArgs struct {
	Query      string `json:"query"`
	MaxResults *int   `json:"max_results"`
}

func parseWebSearchArgs(raw json.RawMessage) (string, int, error) {
	var p webSearchArgs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return "", 0, fmt.Errorf("invalid web_search arguments: %w", err)
	}

	if !utf8.ValidString(p.Query) {
		return "", 0, errors.New("query must be valid UTF-8")
	}
	if strings.ContainsAny(p.Query, "\x00\r\n") {
		return "", 0, errors.New("query contains invalid control characters")
	}
	q := strings.TrimSpace(p.Query)
	if q == "" {
		return "", 0, errors.New("query cannot be empty")
	}
	if len(q) > maxSearchQueryBytes {
		return "", 0, fmt.Errorf("query length %d bytes exceeds %d-byte limit", len(q), maxSearchQueryBytes)
	}
	if runeCount := utf8.RuneCountInString(q); runeCount > maxSearchQueryRunes {
		return "", 0, fmt.Errorf("query length %d characters exceeds %d-character limit", runeCount, maxSearchQueryRunes)
	}

	maxRes := defaultSearchMaxResults
	if p.MaxResults != nil {
		maxRes = *p.MaxResults
		if maxRes < minSearchMaxResults || maxRes > maxSearchMaxResults {
			return "", 0, fmt.Errorf("max_results must be between %d and %d (got %d)", minSearchMaxResults, maxSearchMaxResults, maxRes)
		}
	}
	return q, maxRes, nil
}

func (r *Registry) executeWebSearch(ctx context.Context, rawArgs json.RawMessage) (string, error) {
	if r.webConfig == nil {
		return "", errors.New("web_search is disabled")
	}

	query, maxResults, err := parseWebSearchArgs(rawArgs)
	if err != nil {
		return "", err
	}

	reqCtx, cancel := context.WithTimeout(ctx, webRequestTimeout)
	defer cancel()

	cfg := r.webConfig
	secretKey := cfg.APIKey.raw()
	client := createSearchHTTPClient(cfg.netHooks)

	var items []webSearchItem
	var providerErr error

	switch cfg.Provider {
	case "brave":
		items, providerErr = executeBraveSearch(reqCtx, client, cfg.Endpoint, secretKey, query, maxResults)
	case "tavily":
		items, providerErr = executeTavilySearch(reqCtx, client, cfg.Endpoint, secretKey, query, maxResults)
	case "searxng":
		items, providerErr = executeSearXNGSearch(reqCtx, client, cfg.Endpoint, query, maxResults)
	case "firecrawl":
		items, providerErr = executeFirecrawlSearch(reqCtx, client, cfg.Endpoint, secretKey, query, maxResults)
	default:
		return "", fmt.Errorf("unsupported search provider %q", cfg.Provider)
	}

	if providerErr != nil {
		return "", sanitizeNetworkError(providerErr, secretKey, query)
	}

	truncated := false
	if len(items) > maxResults {
		items = items[:maxResults]
		truncated = true
	}

	return buildSearchOutput(cfg.Provider, items, truncated)
}

func executeBraveSearch(ctx context.Context, client *http.Client, endpoint, apiKey, query string, maxResults int) ([]webSearchItem, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	qs := u.Query()
	qs.Set("q", query)
	qs.Set("count", fmt.Sprintf("%d", maxResults))
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, formatProviderHTTPError("brave", resp, apiKey, query)
	}

	body, _, err := readBoundedBody(resp, maxSearchResponseBytes, maxSearchResponseBytes)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("invalid brave search JSON response: %w", err)
	}

	items := make([]webSearchItem, 0, len(parsed.Web.Results))
	for _, res := range parsed.Web.Results {
		items = append(items, webSearchItem{
			Title:   res.Title,
			URL:     res.URL,
			Snippet: res.Description,
		})
	}
	return items, nil
}

func executeTavilySearch(ctx context.Context, client *http.Client, endpoint, apiKey, query string, maxResults int) ([]webSearchItem, error) {
	payload := struct {
		Query             string `json:"query"`
		MaxResults        int    `json:"max_results"`
		SearchDepth       string `json:"search_depth"`
		IncludeAnswer     bool   `json:"include_answer"`
		IncludeRawContent bool   `json:"include_raw_content"`
		IncludeImages     bool   `json:"include_images"`
	}{
		Query:             query,
		MaxResults:        maxResults,
		SearchDepth:       "basic",
		IncludeAnswer:     false,
		IncludeRawContent: false,
		IncludeImages:     false,
	}
	reqBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, formatProviderHTTPError("tavily", resp, apiKey, query)
	}

	body, _, err := readBoundedBody(resp, maxSearchResponseBytes, maxSearchResponseBytes)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("invalid tavily search JSON response: %w", err)
	}

	items := make([]webSearchItem, 0, len(parsed.Results))
	for _, res := range parsed.Results {
		items = append(items, webSearchItem{
			Title:   res.Title,
			URL:     res.URL,
			Snippet: res.Content,
		})
	}
	return items, nil
}

func executeSearXNGSearch(ctx context.Context, client *http.Client, baseURL, query string, maxResults int) ([]webSearchItem, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/search"
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	qs := u.Query()
	qs.Set("q", query)
	qs.Set("format", "json")
	qs.Set("categories", "general")
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		return nil, errors.New("searxng returned HTTP 403 Forbidden (ensure format=json is enabled on this SearXNG instance)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, formatProviderHTTPError("searxng", resp, "", query)
	}

	body, _, err := readBoundedBody(resp, maxSearchResponseBytes, maxSearchResponseBytes)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("invalid searxng JSON response: %w", err)
	}

	items := make([]webSearchItem, 0, len(parsed.Results))
	for _, res := range parsed.Results {
		items = append(items, webSearchItem{
			Title:   res.Title,
			URL:     res.URL,
			Snippet: res.Content,
		})
	}
	return items, nil
}

func executeFirecrawlSearch(ctx context.Context, client *http.Client, baseURL, apiKey, query string, maxResults int) ([]webSearchItem, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/v1/search"
	payload := struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}{
		Query: query,
		Limit: maxResults,
	}
	reqBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, formatProviderHTTPError("firecrawl", resp, apiKey, query)
	}

	body, _, err := readBoundedBody(resp, maxSearchResponseBytes, maxSearchResponseBytes)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Data    []struct {
			URL         string `json:"url"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Markdown    string `json:"markdown"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("invalid firecrawl search JSON response: %w", err)
	}
	if !parsed.Success && parsed.Error != "" {
		return nil, fmt.Errorf("firecrawl search failed: %s", sanitizeErrorText(parsed.Error, apiKey, query))
	}

	items := make([]webSearchItem, 0, len(parsed.Data))
	for _, res := range parsed.Data {
		snippet := res.Description
		if strings.TrimSpace(snippet) == "" && res.Markdown != "" {
			snippet, _ = clipValidUTF8(res.Markdown, 400)
		}
		items = append(items, webSearchItem{
			Title:   res.Title,
			URL:     res.URL,
			Snippet: snippet,
		})
	}
	return items, nil
}

type webFetchArgs struct {
	URL string `json:"url"`
}

func (r *Registry) executeWebFetch(ctx context.Context, rawArgs json.RawMessage) (string, error) {
	if r.webConfig == nil {
		return "", errors.New("web_fetch is disabled")
	}

	var p webFetchArgs
	dec := json.NewDecoder(bytes.NewReader(rawArgs))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return "", fmt.Errorf("invalid web_fetch arguments: %w", err)
	}

	targetURL, err := validateExternalURL(p.URL)
	if err != nil {
		return "", sanitizeNetworkError(err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, webRequestTimeout)
	defer cancel()

	cfg := r.webConfig
	secretKey := cfg.APIKey.raw()
	safeDisplayURL := redactURLWithoutQuery(targetURL.String())

	if cfg.Provider == "firecrawl" {
		// Mandatory pre-flight SSRF DNS check BEFORE passing target URL to Firecrawl /v1/scrape.
		// Ensures internal/private DNS targets are NEVER forwarded to external scraping servers.
		var resolver ipResolver
		if cfg.netHooks != nil {
			resolver = cfg.netHooks.resolver
		}
		if _, dnsErr := resolveAndVerifyPublicIPs(reqCtx, targetURL.Hostname(), resolver); dnsErr != nil {
			return "", sanitizeNetworkError(fmt.Errorf("target URL blocked by SSRF policy: %w", dnsErr), secretKey)
		}
		return executeFirecrawlScrape(reqCtx, cfg, targetURL.String(), safeDisplayURL)
	}

	// Direct hardened HTTPS fetch for brave / tavily / searxng modes
	client := createFetchHTTPClient(cfg.netHooks)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		return "", sanitizeNetworkError(err, secretKey)
	}
	req.Header.Set("Accept", "text/html, application/xhtml+xml, text/plain;q=0.9, text/markdown;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	req.Header.Set("User-Agent", "NabdCoder/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", sanitizeNetworkError(err, secretKey)
	}
	defer resp.Body.Close()

	finalDisplayURL := safeDisplayURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalDisplayURL = redactURLWithoutQuery(resp.Request.URL.String())
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", formatProviderHTTPError("web_fetch", resp, secretKey, "")
	}

	rawBody, sourceTruncated, err := readBoundedBody(resp, maxCompressedBodyBytes, maxDecompressedBodyBytes)
	if err != nil {
		return "", sanitizeNetworkError(err, secretKey)
	}

	mediaType, decodedText, err := validateContentTypeAndCharset(resp.Header.Get("Content-Type"), rawBody)
	if err != nil {
		return "", err
	}

	var pageTitle string
	bodyText := decodedText
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		pageTitle, bodyText = extractHTMLText(decodedText)
	}

	return buildFetchOutput("", finalDisplayURL, mediaType, pageTitle, bodyText, sourceTruncated)
}

func executeFirecrawlScrape(ctx context.Context, cfg *WebConfig, fullTargetURL, safeDisplayURL string) (string, error) {
	secretKey := cfg.APIKey.raw()
	endpoint := strings.TrimRight(cfg.Endpoint, "/") + "/v1/scrape"

	payload := struct {
		URL     string   `json:"url"`
		Formats []string `json:"formats"`
	}{
		URL:     fullTargetURL,
		Formats: []string{"markdown"},
	}
	reqBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	// Uses createSearchHTTPClient so redirects on Firecrawl endpoint bearing API key are blocked
	client := createSearchHTTPClient(cfg.netHooks)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBytes))
	if err != nil {
		return "", sanitizeNetworkError(err, secretKey)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+secretKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", sanitizeNetworkError(err, secretKey)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", sanitizeNetworkError(formatProviderHTTPError("firecrawl", resp, secretKey, ""), secretKey)
	}

	body, sourceTruncated, err := readBoundedBody(resp, maxCompressedBodyBytes, maxDecompressedBodyBytes)
	if err != nil {
		return "", sanitizeNetworkError(err, secretKey)
	}

	var parsed struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Data    struct {
			Markdown string `json:"markdown"`
			Metadata struct {
				Title     string `json:"title"`
				SourceURL string `json:"sourceURL"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("invalid firecrawl scrape JSON response: %w", err)
	}
	if !parsed.Success {
		errMsg := parsed.Error
		if errMsg == "" {
			errMsg = "scrape failed"
		}
		return "", fmt.Errorf("firecrawl scrape failed: %s", sanitizeErrorText(errMsg, secretKey, ""))
	}

	if parsed.Data.Metadata.SourceURL != "" {
		if validSrc, err := validateExternalURL(parsed.Data.Metadata.SourceURL); err == nil {
			safeDisplayURL = redactURLWithoutQuery(validSrc.String())
		}
	}

	return buildFetchOutput("firecrawl", safeDisplayURL, "text/markdown", parsed.Data.Metadata.Title, parsed.Data.Markdown, sourceTruncated)
}

func formatProviderHTTPError(provider string, resp *http.Response, secretKey, sensitiveQuery string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	snippet := sanitizeErrorText(string(raw), secretKey, sensitiveQuery)
	if snippet == "" {
		return fmt.Errorf("%s returned HTTP %d", provider, resp.StatusCode)
	}
	return fmt.Errorf("%s returned HTTP %d: %s", provider, resp.StatusCode, snippet)
}

func sanitizeErrorText(raw, secretKey, sensitiveQuery string) string {
	s := redactSecrets(raw, secretKey, sensitiveQuery)
	s = sanitizeExtractedWebText(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s, _ = clipValidUTF8(s, maxProviderErrorSnippet)
	return strings.TrimSpace(s)
}
