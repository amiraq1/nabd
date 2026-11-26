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
	WebSearchProviderEnv      = "NABD_WEB_SEARCH"
	WebBraveAPIKeyEnv         = "NABD_BRAVE_API_KEY"
	WebTavilyAPIKeyEnv        = "NABD_TAVILY_API_KEY"
	WebSearXNGURLEnv          = "NABD_SEARXNG_URL"
	WebFirecrawlAPIKeyEnv     = "NABD_FIRECRAWL_API_KEY"
	WebFirecrawlAPIKeyFileEnv = "NABD_FIRECRAWL_API_KEY_FILE"
	WebFirecrawlBaseURLEnv    = "NABD_FIRECRAWL_BASE_URL"

	defaultBraveEndpoint    = "https://api.search.brave.com/res/v1/web/search"
	defaultTavilyEndpoint   = "https://api.tavily.com/search"
	defaultFirecrawlBaseURL = "https://api.firecrawl.dev"

	maxWebKeyFileBytes      = 4096
	maxWebAPIKeyBytes       = 512
	maxSearchQueryRunes     = 400
	maxSearchQueryBytes     = 1024
	defaultSearchMaxResults = 5
	minSearchMaxResults     = 1
	maxSearchMaxResults     = 10
	maxSearchParsedItems    = 50
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
		if err := validateWebAPIKey(WebBraveAPIKeyEnv, key); err != nil {
			return nil, fmt.Errorf("%s=brave: %w", WebSearchProviderEnv, err)
		}
		return &WebConfig{
			Provider: "brave",
			Endpoint: defaultBraveEndpoint,
			APIKey:   webSecret(key),
		}, nil

	case "tavily":
		key := strings.TrimSpace(os.Getenv(WebTavilyAPIKeyEnv))
		if err := validateWebAPIKey(WebTavilyAPIKeyEnv, key); err != nil {
			return nil, fmt.Errorf("%s=tavily: %w", WebSearchProviderEnv, err)
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
		keyFile := strings.TrimSpace(os.Getenv(WebFirecrawlAPIKeyFileEnv))
		if key != "" && keyFile != "" {
			return nil, fmt.Errorf("both %s and %s are set; specify only one", WebFirecrawlAPIKeyEnv, WebFirecrawlAPIKeyFileEnv)
		}
		if keyFile != "" {
			loaded, err := readWebKeyFile(WebFirecrawlAPIKeyFileEnv, keyFile)
			if err != nil {
				return nil, err
			}
			key = loaded
		} else {
			if key == "" {
				return nil, fmt.Errorf("%s=firecrawl requires %s or %s to be set", WebSearchProviderEnv, WebFirecrawlAPIKeyEnv, WebFirecrawlAPIKeyFileEnv)
			}
			if err := validateWebAPIKey(WebFirecrawlAPIKeyEnv, key); err != nil {
				return nil, fmt.Errorf("%s=firecrawl: %w", WebSearchProviderEnv, err)
			}
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

func validateWebAPIKey(envName, key string) error {
	if key == "" {
		return fmt.Errorf("%s must be set and non-empty", envName)
	}
	if len(key) > maxWebAPIKeyBytes {
		return fmt.Errorf("%s length exceeds %d-byte limit", envName, maxWebAPIKeyBytes)
	}
	for i := 0; i < len(key); i++ {
		b := key[i]
		if b < 33 || b > 126 {
			return fmt.Errorf("%s contains invalid whitespace or control character", envName)
		}
	}
	return nil
}

func readWebKeyFile(envName, path string) (string, error) {
	if strings.ContainsAny(path, "\x00\r\n") {
		return "", fmt.Errorf("invalid %s path: contains NUL or newline", envName)
	}
	f, err := openSafeWebKeyFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", envName, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxWebKeyFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", envName, err)
	}
	if len(data) > maxWebKeyFileBytes {
		return "", fmt.Errorf("reading %s: file exceeds %d-byte limit", envName, maxWebKeyFileBytes)
	}
	key := strings.TrimSpace(strings.TrimRight(string(data), "\r\n"))
	if err := validateWebAPIKey(envName, key); err != nil {
		return "", err
	}
	return key, nil
}

// WebSearchProvider returns the active search provider name or "disabled".
func (r *Registry) WebSearchProvider() string {
	if r == nil || r.webConfig == nil || !r.Has("web_search") {
		return "disabled"
	}
	return r.webConfig.Provider
}

// WebSearchDestinationHost returns the sanitized endpoint host (and optional
// non-default port) for the active search provider, or "" when disabled.
func (r *Registry) WebSearchDestinationHost() string {
	if r == nil || r.webConfig == nil || !r.Has("web_search") {
		return ""
	}
	u, err := url.Parse(r.webConfig.Endpoint)
	if err != nil {
		return ""
	}
	return u.Host
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

func decodeStrictJSONObject(raw json.RawMessage, dst any) error {
	if len(raw) > maxApprovalJSONBytes {
		return fmt.Errorf("arguments exceed %d-byte limit", maxApprovalJSONBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return errors.New("unexpected trailing JSON data")
	}
	return nil
}

const maxApprovalJSONBytes = 16 << 10

func parseWebSearchArgs(raw json.RawMessage) (string, int, error) {
	var p webSearchArgs
	if err := decodeStrictJSONObject(raw, &p); err != nil {
		return "", 0, fmt.Errorf("invalid web_search arguments: %w", err)
	}

	if !utf8.ValidString(p.Query) {
		return "", 0, errors.New("query must be valid UTF-8")
	}
	if len(p.Query) > maxSearchQueryBytes {
		return "", 0, fmt.Errorf("query length %d bytes exceeds %d-byte limit", len(p.Query), maxSearchQueryBytes)
	}
	for _, r := range p.Query {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return "", 0, errors.New("query contains invalid control characters")
		}
	}
	q := strings.TrimSpace(p.Query)
	if q == "" {
		return "", 0, errors.New("query cannot be empty")
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

	reqCtx, cancel := context.WithTimeout(ctx, webDirectTimeout)
	defer cancel()

	cfg := r.webConfig
	secretKey := cfg.APIKey.raw()
	client := createSearchHTTPClient(cfg.netHooks, webDirectTimeout)
	defer client.CloseIdleConnections()

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
		return "", sanitizeSearchError(providerErr, secretKey, query)
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

	items := make([]webSearchItem, 0, min(len(parsed.Web.Results), maxSearchParsedItems))
	for _, res := range parsed.Web.Results {
		if len(items) >= maxSearchParsedItems {
			break
		}
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

	items := make([]webSearchItem, 0, min(len(parsed.Results), maxSearchParsedItems))
	for _, res := range parsed.Results {
		if len(items) >= maxSearchParsedItems {
			break
		}
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

	items := make([]webSearchItem, 0, min(len(parsed.Results), maxSearchParsedItems))
	for _, res := range parsed.Results {
		if len(items) >= maxSearchParsedItems {
			break
		}
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

	items := make([]webSearchItem, 0, min(len(parsed.Data), maxSearchParsedItems))
	for _, res := range parsed.Data {
		if len(items) >= maxSearchParsedItems {
			break
		}
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

// CanonicalWebFetchURL validates rawArgs for web_fetch with strict JSON
// decoding and external HTTPS rules, returning the canonical URL string,
// parsed URL, and canonical approval identity digest.
func CanonicalWebFetchURL(rawArgs json.RawMessage) (canonicalURL string, parsed *url.URL, identity string, err error) {
	var p webFetchArgs
	if err := decodeStrictJSONObject(rawArgs, &p); err != nil {
		return "", nil, "", fmt.Errorf("invalid web_fetch arguments: %w", err)
	}
	targetURL, err := validateExternalURL(p.URL)
	if err != nil {
		return "", nil, "", err
	}
	canonical := targetURL.String()
	return canonical, targetURL, "webfetch:sha256:" + hashBytes([]byte(canonical)), nil
}

func (r *Registry) executeWebFetch(ctx context.Context, rawArgs json.RawMessage) (string, error) {
	if r.webConfig == nil {
		return "", errors.New("web_fetch is disabled")
	}

	cfg := r.webConfig
	secretKey := cfg.APIKey.raw()

	canonicalURL, targetURL, _, err := CanonicalWebFetchURL(rawArgs)
	if err != nil {
		return "", sanitizeNetworkError(err, secretKey)
	}

	safeDisplayURL := redactURLWithoutQuery(canonicalURL)

	if cfg.Provider == "firecrawl" {
		reqCtx, cancel := context.WithTimeout(ctx, webFirecrawlScrapeTimeout)
		defer cancel()

		// Conservative pre-flight local SSRF/DNS check BEFORE sending target URL to Firecrawl.
		// Reduces risk only; Nabd cannot enforce DNS pinning, redirects, or subresource SSRF inside Firecrawl.
		var resolver ipResolver
		if cfg.netHooks != nil {
			resolver = cfg.netHooks.resolver
		}
		if _, dnsErr := resolveAndVerifyPublicIPs(reqCtx, targetURL.Hostname(), resolver); dnsErr != nil {
			return "", sanitizeNetworkError(fmt.Errorf("target URL blocked by SSRF policy: %w", dnsErr), secretKey)
		}
		return executeFirecrawlScrape(reqCtx, cfg, targetURL.String(), safeDisplayURL)
	}

	reqCtx, cancel := context.WithTimeout(ctx, webDirectTimeout)
	defer cancel()

	// Direct hardened HTTPS fetch for brave / tavily / searxng modes
	client := createFetchHTTPClient(cfg.netHooks, webDirectTimeout)
	defer client.CloseIdleConnections()

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
	client := createSearchHTTPClient(cfg.netHooks, webFirecrawlScrapeTimeout)
	defer client.CloseIdleConnections()

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
	if sourceTruncated {
		return "", fmt.Errorf("firecrawl scrape response exceeds %d-byte limit", maxDecompressedBodyBytes)
	}

	var parsed struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Data    struct {
			Markdown string `json:"markdown"`
			Metadata struct {
				Title      string `json:"title"`
				SourceURL  string `json:"sourceURL"`
				StatusCode int    `json:"statusCode"`
				Error      string `json:"error"`
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
	if parsed.Data.Metadata.StatusCode >= 400 && strings.TrimSpace(parsed.Data.Markdown) == "" {
		return "", fmt.Errorf("firecrawl target returned HTTP %d", parsed.Data.Metadata.StatusCode)
	}
	if parsed.Data.Metadata.Error != "" && strings.TrimSpace(parsed.Data.Markdown) == "" {
		return "", fmt.Errorf("firecrawl target error: %s", sanitizeErrorText(parsed.Data.Metadata.Error, secretKey, ""))
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
	s := redactSecrets(raw, secretKey)
	s = redactSensitiveQuery(s, sensitiveQuery)
	s = sanitizeExtractedWebText(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s, _ = clipValidUTF8(s, maxProviderErrorSnippet)
	return strings.TrimSpace(s)
}
