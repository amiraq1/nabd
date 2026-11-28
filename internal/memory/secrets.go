package memory

import (
	"regexp"
	"strings"
)

// AdvisorySecretPatterns returns copies of the heuristic patterns used to detect
// likely credentials before saving memories or disclosing tool outputs to a model.
// These heuristics are never comprehensive and must not be presented as a guarantee.
var advisorySecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{16,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bghp_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
	regexp.MustCompile(`\bfc-[A-Za-z0-9_-]{16,}\b`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\b(api[_-]?key|secret|token|password|passwd)\b\s*[:=]\s*\S{8,}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
}

// AdvisorySecretWarnings returns advisory warnings for any credential-like
// patterns found in content. It never includes matched secret substrings.
func AdvisorySecretWarnings(content string) []string {
	var out []string
	for _, pattern := range advisorySecretPatterns {
		if pattern.MatchString(content) {
			out = append(out, "advisory: text resembles a credential ("+pattern.String()+"); this heuristic is not comprehensive")
		}
	}
	return out
}

// ContainsAdvisorySecret reports whether any provided field matches an advisory
// secret heuristic pattern.
func ContainsAdvisorySecret(fields ...string) bool {
	for _, field := range fields {
		if field == "" {
			continue
		}
		for _, pattern := range advisorySecretPatterns {
			if pattern.MatchString(field) {
				return true
			}
		}
	}
	return false
}

// CheckSaveSecrets rejects save inputs when title, body, or tags match any
// advisory secret heuristic, returning a generic error without echoing values.
func CheckSaveSecrets(title, body string, tags []string) error {
	if ContainsAdvisorySecret(title, body) || ContainsAdvisorySecret(tags...) {
		return ErrSecretDetected
	}
	if len(tags) > 0 && ContainsAdvisorySecret(strings.Join(tags, " ")) {
		return ErrSecretDetected
	}
	return nil
}
