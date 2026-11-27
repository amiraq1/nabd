package main

import (
	"math"
	"strings"
	"testing"

	"github.com/amiraq1/nabd/internal/llm"
)

func cacheUsage(prompt, cached int) llm.Usage {
	return llm.Usage{
		Reported:           true,
		PromptReported:     true,
		PromptTokens:       prompt,
		CachedPromptTokens: &cached,
	}
}

func TestCacheStatsUsesSumsNotAverageOfRatios(t *testing.T) {
	var stats cacheStats
	stats.observe(cacheUsage(100, 50))  // 50%
	stats.observe(cacheUsage(300, 300)) // 100%
	if stats.comparable != 2 || stats.reported != 2 {
		t.Fatalf("coverage = %d/%d", stats.comparable, stats.reported)
	}
	if stats.prompt != 400 || stats.cached != 350 {
		t.Fatalf("sums = %d/%d, want 350/400", stats.cached, stats.prompt)
	}
	// Average of ratios would be 75%; the documented sum is 87.5% -> 88%.
	if got := stats.statusLine(); got != "Cache: 88% 350/400 (2/2)" {
		t.Fatalf("status = %q", got)
	}
}

func TestCacheStatsCoverageInvalidAndNoData(t *testing.T) {
	var stats cacheStats
	if got := stats.statusLine(); got != "Cache: n/a" {
		t.Fatalf("empty status = %q", got)
	}

	stats.observe(llm.Usage{})
	stats.observe(llm.Usage{Reported: true, PromptReported: true, PromptTokens: 100})
	stats.observe(cacheUsage(100, 50))
	if got := stats.statusLine(); got != "Cache: 50% 50/100 (1/2)" {
		t.Fatalf("mixed coverage status = %q", got)
	}

	var invalid cacheStats
	invalid.observe(llm.Usage{Reported: true, Invalid: true})
	if got := invalid.statusLine(); got != "Cache: n/a (1 invalid)" {
		t.Fatalf("invalid status = %q", got)
	}

	var zero cacheStats
	zero.observe(cacheUsage(100, 0))
	if got := zero.statusLine(); got != "Cache: 0% 0/100 (1/1)" {
		t.Fatalf("explicit zero status = %q", got)
	}

	var zeroPrompt cacheStats
	zeroPrompt.observe(cacheUsage(0, 0))
	if got := zeroPrompt.statusLine(); !strings.HasPrefix(got, "Cache: n/a") {
		t.Fatalf("zero denominator status = %q", got)
	}
}

func TestCacheStatsRejectsOverflowingSums(t *testing.T) {
	var stats cacheStats
	stats.observe(cacheUsage(math.MaxInt, math.MaxInt))
	stats.observe(cacheUsage(math.MaxInt, 0))
	if stats.comparable != 1 || stats.invalid != 1 {
		t.Fatalf("overflow was not excluded: comparable=%d invalid=%d", stats.comparable, stats.invalid)
	}
	if stats.prompt < 0 || stats.cached < 0 {
		t.Fatalf("counter wrapped: %+v", stats)
	}
}

func TestCacheStatusLineFitsNarrowTerminal(t *testing.T) {
	var stats cacheStats
	stats.observe(cacheUsage(1_000_000, 999_999))
	stats.observe(cacheUsage(1_000_000, 0))
	for _, line := range []string{
		stats.statusLine(),
		(cacheStats{}).statusLine(),
		(cacheStats{reported: 9}).statusLine(),
		(cacheStats{invalid: 2}).statusLine(),
	} {
		if len(line) > 36 {
			t.Fatalf("cache line exceeds 36 columns (%d): %q", len(line), line)
		}
		if strings.ContainsAny(line, "\x1b\r\n") {
			t.Fatalf("cache line contains control characters: %q", line)
		}
	}
}

func TestCacheStatsResetOnClear(t *testing.T) {
	m := initialModel(nil, &mockClient{})
	m.cache.observe(cacheUsage(100, 40))
	if m.cache.reported != 1 {
		t.Fatal("cache stats were not recorded")
	}
	updated, _ := m.executeLocalCommand("/clear")
	cleared := updated.(uiModel)
	if cleared.cache != (cacheStats{}) {
		t.Fatalf("/clear did not reset cache stats: %+v", cleared.cache)
	}
}

func TestLateLLMResponseDoesNotPolluteCacheStats(t *testing.T) {
	m := uiModel{
		taskGen: 7,
		messages: []Message{
			{Role: "system", Content: "system"},
		},
	}
	late, _ := m.Update(llmResponseMsg{gen: 6, usage: cacheUsage(100, 40)})
	if got := late.(uiModel).cache.reported; got != 0 {
		t.Fatalf("stale response was counted (%d reported)", got)
	}

	fresh, _ := m.Update(llmResponseMsg{
		gen:          7,
		assistantMsg: Message{Role: "assistant", Content: "done"},
		usage:        cacheUsage(100, 40),
	})
	if got := fresh.(uiModel).cache; got.reported != 1 || got.comparable != 1 || got.prompt != 100 || got.cached != 40 {
		t.Fatalf("fresh response was not counted once: %+v", got)
	}
}

func TestStatusShowsCacheWithoutConversationText(t *testing.T) {
	m := uiModel{
		messages: []Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "SECRET-QUERY-TEXT"},
		},
		historyBudget: defaultHistoryTokens,
	}
	m.cache.observe(cacheUsage(100, 40))
	status := m.formatStatus()
	if !strings.Contains(status, "Cache: 40% 40/100 (1/1)") {
		t.Fatalf("status is missing the cache line:\n%s", status)
	}
	if strings.Contains(status, "SECRET-QUERY-TEXT") {
		t.Fatal("status leaked conversation content")
	}
}
