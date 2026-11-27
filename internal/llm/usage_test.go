package llm

import (
	"encoding/json"
	"fmt"
	"testing"
)

func openAIResponseWithUsage(t *testing.T, usage string) *Response {
	t.Helper()
	body := fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]%s}`, usage)
	resp, err := parseResponse([]byte(body))
	if err != nil {
		t.Fatalf("parseResponse(%s): %v", usage, err)
	}
	return resp
}

func geminiResponseWithUsage(t *testing.T, usage string) *Response {
	t.Helper()
	body := fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]%s}`, usage)
	resp, err := parseGeminiResponse([]byte(body))
	if err != nil {
		t.Fatalf("parseGeminiResponse(%s): %v", usage, err)
	}
	return resp
}

func cacheInt(value int) *int { return &value }

func TestOpenAIUsageCacheFieldParsing(t *testing.T) {
	cases := []struct {
		name        string
		usage       string
		reported    bool
		promptKnown bool
		prompt      int
		cached      *int
		invalid     bool
		cacheUsable bool
	}{
		{
			name:        "nested cached_tokens",
			usage:       `,"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_tokens_details":{"cached_tokens":40}}`,
			reported:    true,
			promptKnown: true,
			prompt:      100,
			cached:      cacheInt(40),
			cacheUsable: true,
		},
		{
			name:        "deepseek flat prompt_cache_hit_tokens",
			usage:       `,"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_cache_hit_tokens":30}`,
			reported:    true,
			promptKnown: true,
			prompt:      100,
			cached:      cacheInt(30),
			cacheUsable: true,
		},
		{
			name:        "both fields agree",
			usage:       `,"usage":{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":40},"prompt_cache_hit_tokens":40}`,
			reported:    true,
			promptKnown: true,
			prompt:      100,
			cached:      cacheInt(40),
			cacheUsable: true,
		},
		{
			name:        "both fields conflict",
			usage:       `,"usage":{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":40},"prompt_cache_hit_tokens":30}`,
			reported:    true,
			promptKnown: true,
			prompt:      100,
			cached:      cacheInt(40),
			invalid:     true,
			cacheUsable: false,
		},
		{
			name:        "explicit zero cached_tokens",
			usage:       `,"usage":{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":0}}`,
			reported:    true,
			promptKnown: true,
			prompt:      100,
			cached:      cacheInt(0),
			cacheUsable: true,
		},
		{
			name:        "no cache field reported",
			usage:       `,"usage":{"prompt_tokens":100,"completion_tokens":10}`,
			reported:    true,
			promptKnown: true,
			prompt:      100,
			cacheUsable: false,
		},
		{
			name:        "no usage object",
			usage:       ``,
			reported:    false,
			cacheUsable: false,
		},
		{
			name:        "null usage",
			usage:       `,"usage":null`,
			reported:    false,
			cacheUsable: false,
		},
		{
			name:        "malformed usage does not fail the response",
			usage:       `,"usage":"not-an-object"`,
			reported:    true,
			invalid:     true,
			cacheUsable: false,
		},
		{
			name:        "negative cached count",
			usage:       `,"usage":{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":-1}}`,
			reported:    true,
			promptKnown: true,
			prompt:      100,
			invalid:     true,
			cacheUsable: false,
		},
		{
			name:        "cached greater than prompt",
			usage:       `,"usage":{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":11}}`,
			reported:    true,
			promptKnown: true,
			prompt:      10,
			cached:      cacheInt(11),
			invalid:     true,
			cacheUsable: false,
		},
		{
			name:        "overflowing count",
			usage:       `,"usage":{"prompt_tokens":999999999999999999999999}`,
			reported:    true,
			promptKnown: true, // the field was present, but its value is not trusted
			invalid:     true,
			cacheUsable: false,
		},
		{
			name:        "cached without prompt",
			usage:       `,"usage":{"prompt_tokens_details":{"cached_tokens":5}}`,
			reported:    true,
			promptKnown: false,
			cached:      cacheInt(5),
			cacheUsable: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := openAIResponseWithUsage(t, tc.usage)
			got := resp.Usage
			if got.Reported != tc.reported {
				t.Fatalf("Reported = %v, want %v", got.Reported, tc.reported)
			}
			if got.PromptReported != tc.promptKnown {
				t.Fatalf("PromptReported = %v, want %v", got.PromptReported, tc.promptKnown)
			}
			if tc.promptKnown && got.PromptTokens != tc.prompt {
				t.Fatalf("PromptTokens = %d, want %d", got.PromptTokens, tc.prompt)
			}
			if got.Invalid != tc.invalid {
				t.Fatalf("Invalid = %v, want %v", got.Invalid, tc.invalid)
			}
			if tc.cached != nil && got.CachedPromptTokens != nil && *got.CachedPromptTokens != *tc.cached {
				t.Fatalf("CachedPromptTokens = %d, want %d", *got.CachedPromptTokens, *tc.cached)
			}
			cached, usable := got.CacheTokens()
			if usable != tc.cacheUsable {
				t.Fatalf("CacheTokens usable = %v, want %v (value %d)", usable, tc.cacheUsable, cached)
			}
			if usable && tc.cached != nil && cached != *tc.cached {
				t.Fatalf("CacheTokens = %d, want %d", cached, *tc.cached)
			}
		})
	}
}

func TestGeminiUsageCacheFieldParsing(t *testing.T) {
	cases := []struct {
		name        string
		usage       string
		reported    bool
		promptKnown bool
		prompt      int
		cached      *int
		invalid     bool
		cacheUsable bool
	}{
		{
			name:        "full metadata",
			usage:       `,"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":10,"totalTokenCount":110,"cachedContentTokenCount":40}`,
			reported:    true,
			promptKnown: true,
			prompt:      100,
			cached:      cacheInt(40),
			cacheUsable: true,
		},
		{
			name:        "partial metadata",
			usage:       `,"usageMetadata":{"promptTokenCount":100}`,
			reported:    true,
			promptKnown: true,
			prompt:      100,
			cacheUsable: false,
		},
		{
			name:        "explicit zero cache",
			usage:       `,"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":0}`,
			reported:    true,
			promptKnown: true,
			prompt:      100,
			cached:      cacheInt(0),
			cacheUsable: true,
		},
		{
			name:        "no metadata",
			usage:       ``,
			reported:    false,
			cacheUsable: false,
		},
		{
			name:        "malformed metadata",
			usage:       `,"usageMetadata":[1,2]`,
			reported:    true,
			invalid:     true,
			cacheUsable: false,
		},
		{
			name:        "negative cache count",
			usage:       `,"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":-5}`,
			reported:    true,
			promptKnown: true,
			prompt:      100,
			invalid:     true,
			cacheUsable: false,
		},
		{
			name:        "cache greater than prompt",
			usage:       `,"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":11}`,
			reported:    true,
			promptKnown: true,
			prompt:      10,
			invalid:     true,
			cacheUsable: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := geminiResponseWithUsage(t, tc.usage)
			got := resp.Usage
			if got.Reported != tc.reported {
				t.Fatalf("Reported = %v, want %v", got.Reported, tc.reported)
			}
			if got.PromptReported != tc.promptKnown {
				t.Fatalf("PromptReported = %v, want %v", got.PromptReported, tc.promptKnown)
			}
			if tc.promptKnown && got.PromptTokens != tc.prompt {
				t.Fatalf("PromptTokens = %d, want %d", got.PromptTokens, tc.prompt)
			}
			if got.Invalid != tc.invalid {
				t.Fatalf("Invalid = %v, want %v", got.Invalid, tc.invalid)
			}
			cached, usable := got.CacheTokens()
			if usable != tc.cacheUsable {
				t.Fatalf("CacheTokens usable = %v, want %v", usable, tc.cacheUsable)
			}
			if usable && tc.cached != nil && cached != *tc.cached {
				t.Fatalf("CacheTokens = %d, want %d", cached, *tc.cached)
			}
		})
	}
}

func TestUsageJSONDoesNotLeakProviderFields(t *testing.T) {
	resp := openAIResponseWithUsage(t, `,"usage":{"prompt_tokens":1,"prompt_tokens_details":{"cached_tokens":1}}`)
	if got := resp.Usage.Reported; !got {
		t.Fatal("usage presence was not recorded")
	}
	encoded, err := json.Marshal(resp.Usage)
	if err != nil {
		t.Fatal(err)
	}
	// New bookkeeping fields are internal and must not appear if Usage is ever
	// serialized by mistake.
	for _, forbidden := range []string{"Reported", "CachedPromptTokens", "PromptCacheHitTokens", "Invalid"} {
		if containsField(encoded, forbidden) {
			t.Fatalf("serialized Usage leaked internal field %q: %s", forbidden, encoded)
		}
	}
}

func containsField(data []byte, field string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	_, ok := raw[field]
	return ok
}
