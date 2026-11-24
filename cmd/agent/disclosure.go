package main

import (
	"regexp"
	"strings"
)

// These are advisory heuristics only. They are not a secret scanner and must
// never be presented as a guarantee: they miss unfamiliar formats, encoded
// values and split keys, and they can flag harmless text. The approval gate
// remains the user's review of the exact displayed bytes.
var advisorySecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bghp_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\b(api[_-]?key|secret|token|password|passwd)\b\s*[:=]\s*\S{8,}`),
}

func advisorySecretWarnings(content string) []string {
	var out []string
	for _, pattern := range advisorySecretPatterns {
		if pattern.MatchString(content) {
			out = append(out, "advisory: text resembles a credential ("+pattern.String()+"); this heuristic is not comprehensive")
		}
	}
	return out
}

func disclosureReviewText(content string) string {
	warnings := advisorySecretWarnings(content)
	if len(warnings) == 0 {
		return content
	}
	return strings.Join(warnings, "\n") + "\n" + content
}
