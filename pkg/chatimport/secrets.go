package chatimport

import (
	"regexp"
)

// Secret detection is warn-only by project decision (2026-09-20): imported
// content is stored verbatim and findings are reported in the run ledger.
// Patterns err on the specific side — high-entropy generic matching would
// flood the warnings with source code and identifiers.
var secretPatterns = []struct {
	name  string
	regex *regexp.Regexp
}{
	{"OpenAI API key", regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{20,}`)},
	{"Anthropic API key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}|\bgithub_pat_[A-Za-z0-9_]{20,}`)},
	{"AWS access key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`)},
	{"Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`)},
	{"JWT", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	{"private key block", regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----`)},
	{"bearer token", regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._+/=-]{24,}`)},
}

// ScanSecrets returns the names of secret patterns present in content.
// It never returns the matched text.
func ScanSecrets(content string) []string {
	if content == "" {
		return nil
	}
	var found []string
	for _, p := range secretPatterns {
		if p.regex.MatchString(content) {
			found = append(found, p.name)
		}
	}
	return found
}
