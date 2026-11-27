package main

import (
	"fmt"
	"math"
	"strconv"

	"github.com/amiraq1/nabd/internal/llm"
)

// cacheStats aggregates only provider-reported token counts for this session.
// The ratio uses sum(cached)/sum(prompt) over responses that reported both
// values, never an average of per-response ratios, so a mix of large and small
// prompts cannot skew it.
type cacheStats struct {
	// reported counts successful responses that included a usage object.
	reported int
	// comparable counts responses that reported prompt and a usable cache count.
	comparable int
	// invalid counts responses whose usage was negative, conflicting or out of
	// range; they are excluded from the ratio instead of becoming a fake zero.
	invalid int
	prompt  int64
	cached  int64
}

func (s *cacheStats) observe(usage llm.Usage) {
	if !usage.Reported {
		return
	}
	s.reported++
	if usage.Invalid {
		s.invalid++
		return
	}
	cachedTokens, ok := usage.CacheTokens()
	if !ok || !usage.PromptReported {
		return
	}
	// A sum that would overflow is not a trustworthy measurement; keep the
	// session visible as partially invalid instead of wrapping silently.
	if s.prompt > math.MaxInt64-int64(usage.PromptTokens) || s.cached > math.MaxInt64-int64(cachedTokens) {
		s.invalid++
		return
	}
	s.prompt += int64(usage.PromptTokens)
	s.cached += int64(cachedTokens)
	s.comparable++
}

func (s *cacheStats) reset() {
	*s = cacheStats{}
}

// statusLine is deliberately short so it fits a 36-column terminal.
func (s cacheStats) statusLine() string {
	if s.comparable > 0 && s.prompt > 0 {
		percent := float64(s.cached) * 100 / float64(s.prompt)
		return fmt.Sprintf(
			"Cache: %.0f%% %s/%s (%d/%d)",
			percent,
			formatTokenCount(s.cached),
			formatTokenCount(s.prompt),
			s.comparable,
			s.reported,
		)
	}
	if s.invalid > 0 {
		return fmt.Sprintf("Cache: n/a (%d invalid)", s.invalid)
	}
	if s.reported > 0 {
		return fmt.Sprintf("Cache: n/a (0/%d)", s.reported)
	}
	return "Cache: n/a"
}

// formatTokenCount renders provider token counts compactly without implying a
// currency or a cost.
func formatTokenCount(count int64) string {
	switch {
	case count >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(count)/1_000_000_000)
	case count >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(count)/1_000_000)
	case count >= 1_000:
		return fmt.Sprintf("%.1fk", float64(count)/1_000)
	default:
		return strconv.FormatInt(count, 10)
	}
}
