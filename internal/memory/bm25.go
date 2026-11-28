package memory

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	bm25K1      = 1.5
	bm25B       = 0.75
	weightTitle = 2.0
	weightTags  = 1.5
	weightBody  = 1.0
)

func isArabicDiacritic(r rune) bool {
	return (r >= 0x064B && r <= 0x065F) ||
		r == 0x0670 ||
		(r >= 0x06D6 && r <= 0x06ED)
}

// Tokenize splits text into lowercase Unicode letter/number tokens.
// Arabic vowel diacritics (tashkeel) and tatweel (U+0640) are omitted inside
// tokens during indexing/querying without splitting the word or altering stored
// text. Hamza/alef forms, ta-marbuta, and alef-maqsura remain literal.
func Tokenize(s string) []string {
	var tokens []string
	var cur strings.Builder
	inToken := false

	flush := func() {
		if inToken && cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
		inToken = false
	}

	for _, r := range s {
		if r == '\u0640' { // Arabic tatweel: omit without breaking token
			continue
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			cur.WriteRune(unicode.ToLower(r))
			inToken = true
			continue
		}
		if inToken && unicode.Is(unicode.Mn, r) {
			if isArabicDiacritic(r) {
				continue
			}
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return tokens
}

type scoredEntry struct {
	entry     Entry
	score     float64
	createdAt time.Time
}

type indexedDoc struct {
	entry     Entry
	createdAt time.Time
	titleTF   map[string]int
	tagsTF    map[string]int
	bodyTF    map[string]int
	docLen    float64
}

func countTokens(tokens []string) (map[string]int, int) {
	tf := make(map[string]int, len(tokens))
	for _, tok := range tokens {
		tf[tok]++
	}
	return tf, len(tokens)
}

// SearchEntries ranks entries matching query using field-weighted Okapi BM25
// (k1=1.5, b=0.75; weights title=2.0, tags=1.5, body=1.0).
// Ordering is deterministic: score desc, created_at desc, id asc.
func SearchEntries(entries []Entry, query, kindFilter string, maxResults int) ([]SearchMatch, int) {
	if maxResults <= 0 {
		maxResults = DefaultSearchResults
	}
	if maxResults > MaxSearchResults {
		maxResults = MaxSearchResults
	}

	rawQ := Tokenize(query)
	if len(rawQ) == 0 {
		return []SearchMatch{}, 0
	}
	seenQ := make(map[string]struct{}, len(rawQ))
	qTerms := make([]string, 0, len(rawQ))
	for _, qt := range rawQ {
		if _, dup := seenQ[qt]; dup {
			continue
		}
		seenQ[qt] = struct{}{}
		qTerms = append(qTerms, qt)
	}

	docs := make([]indexedDoc, 0, len(entries))
	var totalDocLen float64
	for _, e := range entries {
		if kindFilter != "" && string(e.Kind) != kindFilter {
			continue
		}
		titleTF, titleLen := countTokens(Tokenize(e.Title))
		tagsTF := make(map[string]int)
		tagsLen := 0
		for _, tag := range e.Tags {
			for _, tok := range Tokenize(tag) {
				tagsTF[tok]++
				tagsLen++
			}
		}
		bodyTF, bodyLen := countTokens(Tokenize(e.Body))
		dLen := weightTitle*float64(titleLen) + weightTags*float64(tagsLen) + weightBody*float64(bodyLen)
		parsedAt, _ := time.Parse(time.RFC3339, e.CreatedAt)
		docs = append(docs, indexedDoc{
			entry:     e,
			createdAt: parsedAt,
			titleTF:   titleTF,
			tagsTF:    tagsTF,
			bodyTF:    bodyTF,
			docLen:    dLen,
		})
		totalDocLen += dLen
	}

	nDocs := len(docs)
	if nDocs == 0 {
		return []SearchMatch{}, 0
	}
	avgdl := totalDocLen / float64(nDocs)
	if avgdl <= 0 {
		avgdl = 1
	}

	df := make(map[string]int, len(qTerms))
	for _, qt := range qTerms {
		count := 0
		for i := range docs {
			if docs[i].titleTF[qt] > 0 || docs[i].tagsTF[qt] > 0 || docs[i].bodyTF[qt] > 0 {
				count++
			}
		}
		df[qt] = count
	}

	scored := make([]scoredEntry, 0, nDocs)
	for i := range docs {
		var score float64
		normFactor := 1.0 - bm25B + bm25B*(docs[i].docLen/avgdl)
		for _, qt := range qTerms {
			docFreq := df[qt]
			if docFreq == 0 {
				continue
			}
			pseudoTF := weightTitle*float64(docs[i].titleTF[qt]) +
				weightTags*float64(docs[i].tagsTF[qt]) +
				weightBody*float64(docs[i].bodyTF[qt])
			if pseudoTF == 0 {
				continue
			}
			idf := math.Log((float64(nDocs)-float64(docFreq)+0.5)/(float64(docFreq)+0.5) + 1.0)
			tfNorm := (pseudoTF * (bm25K1 + 1.0)) / (pseudoTF + bm25K1*normFactor)
			score += idf * tfNorm
		}
		if score > 0 {
			scored = append(scored, scoredEntry{
				entry:     docs[i].entry,
				score:     score,
				createdAt: docs[i].createdAt,
			})
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		if !scored[i].createdAt.Equal(scored[j].createdAt) {
			return scored[i].createdAt.After(scored[j].createdAt)
		}
		return scored[i].entry.ID < scored[j].entry.ID
	})

	totalMatches := len(scored)
	limit := min(maxResults, totalMatches)
	results := make([]SearchMatch, 0, limit)
	for i := 0; i < limit; i++ {
		e := scored[i].entry
		results = append(results, SearchMatch{
			ID:        e.ID,
			Kind:      e.Kind,
			Title:     e.Title,
			Body:      e.Body,
			Tags:      append([]string(nil), e.Tags...),
			CreatedAt: e.CreatedAt,
			Score:     math.Round(scored[i].score*1e6) / 1e6,
		})
	}
	return results, totalMatches
}

// MarshalSearchResponse builds a SearchResponse whose JSON encoding strictly fits
// within MaxSearchOutputBytes (8 KiB) with UTF-8-safe truncation.
func MarshalSearchResponse(totalMatches int, matches []SearchMatch) (SearchResponse, []byte, error) {
	if matches == nil {
		matches = []SearchMatch{}
	}
	resp := SearchResponse{
		SecurityNotice: SecurityNotice,
		TotalMatches:   totalMatches,
		Returned:       len(matches),
		Truncated:      totalMatches > len(matches),
		Results:        append([]SearchMatch(nil), matches...),
	}
	encoded, err := json.Marshal(resp)
	if err != nil {
		return SearchResponse{}, nil, err
	}
	if len(encoded) <= MaxSearchOutputBytes {
		return resp, encoded, nil
	}

	// Step 1: shrink body fields in UTF-8-safe steps before dropping whole matches.
	for _, bodyCap := range []int{512, 256, 128, 64} {
		changed := false
		for i := range resp.Results {
			if len(resp.Results[i].Body) > bodyCap {
				resp.Results[i].Body = truncateUTF8WithEllipsis(resp.Results[i].Body, bodyCap)
				changed = true
			}
		}
		if !changed {
			continue
		}
		resp.Truncated = true
		encoded, err = json.Marshal(resp)
		if err != nil {
			return SearchResponse{}, nil, err
		}
		if len(encoded) <= MaxSearchOutputBytes {
			return resp, encoded, nil
		}
	}

	// Step 2: drop trailing matches until valid JSON fits inside MaxSearchOutputBytes.
	for len(encoded) > MaxSearchOutputBytes && len(resp.Results) > 0 {
		resp.Results = resp.Results[:len(resp.Results)-1]
		resp.Returned = len(resp.Results)
		resp.Truncated = true
		encoded, err = json.Marshal(resp)
		if err != nil {
			return SearchResponse{}, nil, err
		}
	}
	return resp, encoded, nil
}

func truncateUTF8WithEllipsis(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 3 {
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		return s[:cut]
	}
	cut := maxBytes - 3
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
