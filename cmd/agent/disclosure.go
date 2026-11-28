package main

import (
	"strings"

	"github.com/amiraq1/nabd/internal/memory"
)

// These are advisory heuristics only. They are not a secret scanner and must
// never be presented as a guarantee: they miss unfamiliar formats, encoded
// values and split keys, and they can flag harmless text. The approval gate
// remains the user's review of the exact displayed bytes.
func advisorySecretWarnings(content string) []string {
	return memory.AdvisorySecretWarnings(content)
}

func disclosureReviewText(content string) string {
	warnings := advisorySecretWarnings(content)
	if len(warnings) == 0 {
		return content
	}
	return strings.Join(warnings, "\n") + "\n" + content
}
