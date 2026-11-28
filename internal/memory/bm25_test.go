package memory

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTokenizeArabicAndEnglish(t *testing.T) {
	// Diacritized and tatweel Arabic forms must normalize to the same base token
	// without breaking words, while hamza/alef variants stay literal.
	t1 := Tokenize("كِتَابُ المَعْلُومَاتِ")
	t2 := Tokenize("كتاب المعلومات")
	t3 := Tokenize("كـتـاب")
	if len(t1) != 2 || len(t2) != 2 || t1[0] != t2[0] || t1[1] != t2[1] {
		t.Fatalf("diacritized Arabic tokenization mismatch: %v vs %v", t1, t2)
	}
	if len(t3) != 1 || t3[0] != "كتاب" {
		t.Fatalf("tatweel Arabic tokenization mismatch: %v", t3)
	}

	// Hamza forms stay literal (no unimplemented hamza folding).
	tHamza := Tokenize("أحمد")
	tBare := Tokenize("احمد")
	if len(tHamza) != 1 || len(tBare) != 1 || tHamza[0] == tBare[0] {
		t.Fatalf("expected literal hamza handling, got %v vs %v", tHamza, tBare)
	}

	// English/alphanumeric lowercasing and Unicode combining marks.
	tEng := Tokenize("Go-1.27 BM25_Index café")
	want := []string{"go", "1", "27", "bm25", "index", "café"}
	if len(tEng) != len(want) {
		t.Fatalf("got %v, want %v", tEng, want)
	}
	for i := range want {
		if tEng[i] != want[i] {
			t.Fatalf("token[%d] = %q, want %q", i, tEng[i], want[i])
		}
	}
}

func TestBM25FieldWeightsAndDeterministicTieBreaking(t *testing.T) {
	base := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	entries := []Entry{
		{
			ID:        "mem_00000001",
			Kind:      KindNote,
			Title:     "General notes",
			Body:      "We discussed sandbox isolation today.",
			Tags:      []string{"ops"},
			CreatedAt: base.Format(time.RFC3339),
		},
		{
			ID:        "mem_00000002",
			Kind:      KindDecision,
			Title:     "Sandbox architecture",
			Body:      "Keep defaults conservative.",
			Tags:      []string{"security"},
			CreatedAt: base.Add(-time.Hour).Format(time.RFC3339),
		},
		{
			ID:        "mem_00000003",
			Kind:      KindDecision,
			Title:     "Architecture review",
			Body:      "Keep defaults conservative.",
			Tags:      []string{"sandbox"},
			CreatedAt: base.Add(-2 * time.Hour).Format(time.RFC3339),
		},
	}

	matches, total := SearchEntries(entries, "sandbox", "", 10)
	if total != 3 || len(matches) != 3 {
		t.Fatalf("expected 3 matches, got total=%d len=%d", total, len(matches))
	}
	// Title weight (2.0) > Tags weight (1.5) > Body weight (1.0).
	if matches[0].ID != "mem_00000002" {
		t.Fatalf("expected title match first, got %s (scores: %+v)", matches[0].ID, matches)
	}
	if matches[1].ID != "mem_00000003" {
		t.Fatalf("expected tag match second, got %s", matches[1].ID)
	}
	if matches[2].ID != "mem_00000001" {
		t.Fatalf("expected body match third, got %s", matches[2].ID)
	}

	// Verify exact BM25 score formula for one term in uniform corpus.
	tied := []Entry{
		{
			ID:        "mem_0000000b",
			Kind:      KindNote,
			Title:     "Alpha",
			Body:      "token",
			CreatedAt: base.Format(time.RFC3339),
		},
		{
			ID:        "mem_0000000a",
			Kind:      KindNote,
			Title:     "Alpha",
			Body:      "token",
			CreatedAt: base.Format(time.RFC3339),
		},
		{
			ID:        "mem_0000000c",
			Kind:      KindNote,
			Title:     "Alpha",
			Body:      "token",
			CreatedAt: base.Add(time.Minute).Format(time.RFC3339),
		},
	}
	tieMatches, _ := SearchEntries(tied, "token", "", 10)
	if len(tieMatches) != 3 {
		t.Fatalf("expected 3 tied matches, got %d", len(tieMatches))
	}
	// Tied scores: newer created_at first (c), then id asc (a before b).
	if tieMatches[0].ID != "mem_0000000c" || tieMatches[1].ID != "mem_0000000a" || tieMatches[2].ID != "mem_0000000b" {
		t.Fatalf("unexpected tie-break order: %v, %v, %v", tieMatches[0].ID, tieMatches[1].ID, tieMatches[2].ID)
	}
	// Check repeated query term does not inflate score.
	dupMatches, _ := SearchEntries(tied, "token token token", "", 10)
	if math.Abs(dupMatches[0].Score-tieMatches[0].Score) > 1e-6 {
		t.Fatalf("duplicate query term changed score: %f vs %f", dupMatches[0].Score, tieMatches[0].Score)
	}
}

func TestSearchKindFilterArabicAndEmptyResults(t *testing.T) {
	base := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	entries := []Entry{
		{
			ID:        "mem_11111111",
			Kind:      KindDecision,
			Title:     "قرار عربي",
			Body:      "استخدام مِقياس مُحدّد للذاكرة المحلية",
			CreatedAt: base.Format(time.RFC3339),
		},
		{
			ID:        "mem_22222222",
			Kind:      KindBugfix,
			Title:     "إصلاح خطأ",
			Body:      "معالجة الذاكرة عند الإقلاع",
			CreatedAt: base.Add(time.Minute).Format(time.RFC3339),
		},
	}

	// Punctuation-only query -> 0 results, valid JSON.
	pMatches, pTotal := SearchEntries(entries, "!!! ???", "", 5)
	if pTotal != 0 || len(pMatches) != 0 {
		t.Fatalf("expected empty matches for punctuation query, got total=%d len=%d", pTotal, len(pMatches))
	}

	// Diacritized vs non-diacritized Arabic search with kind filter.
	matches, total := SearchEntries(entries, "مقياس محدد", string(KindDecision), 5)
	if total != 1 || len(matches) != 1 || matches[0].ID != "mem_11111111" {
		t.Fatalf("expected Arabic decision match, got total=%d matches=%+v", total, matches)
	}
}

func TestMarshalSearchResponseCapsAt8KiBWithValidJSONAndUTF8(t *testing.T) {
	body := strings.Repeat("م", 2000) // 2000 bytes, 1000 Arabic runes
	var matches []SearchMatch
	for i := 0; i < MaxSearchResults; i++ {
		matches = append(matches, SearchMatch{
			ID:        "mem_0000000" + string(rune('0'+i)),
			Kind:      KindNote,
			Title:     "Entry " + string(rune('A'+i)),
			Body:      body,
			Tags:      []string{"arabic", "budget"},
			CreatedAt: "2026-11-28T12:00:00Z",
			Score:     float64(10 - i),
		})
	}

	resp, raw, err := MarshalSearchResponse(12, matches)
	if err != nil {
		t.Fatalf("MarshalSearchResponse: %v", err)
	}
	if len(raw) > MaxSearchOutputBytes {
		t.Fatalf("output exceeded 8 KiB: %d bytes", len(raw))
	}
	if !utf8.Valid(raw) || !json.Valid(raw) {
		t.Fatal("output is not valid UTF-8 JSON")
	}
	if !resp.Truncated {
		t.Fatal("expected truncated=true when bodies/results were clipped to fit 8 KiB")
	}
	if resp.Returned != len(resp.Results) || resp.TotalMatches != 12 {
		t.Fatalf("inaccurate counts: returned=%d results=%d total=%d", resp.Returned, len(resp.Results), resp.TotalMatches)
	}
}
