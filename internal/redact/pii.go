package redact

import (
	"regexp"
	"strings"
	"sync"
)

// PIICategory identifies a category of personally identifying strings
// the redaction pass can mask.
type PIICategory string

const (
	PIIEmail PIICategory = "email"
	PIIPhone PIICategory = "phone"
)

// Label constants used in replacement tokens.
const (
	labelEmail = "EMAIL"
	labelPhone = "PHONE"
)

// piiPattern is a compiled regex with its replacement token label.
type piiPattern struct {
	regex *regexp.Regexp
	label string // e.g., "EMAIL", "PHONE"
}

var (
	piiPatterns   []piiPattern
	piiPatternsMu sync.RWMutex
)

// ConfigurePII selects which PII categories the redaction pass masks, on
// top of the always-on secret layers. Matches are replaced with
// [REDACTED_<CATEGORY>] tokens. Call once at startup; thread-safe.
func ConfigurePII(categories ...PIICategory) {
	patterns := make([]piiPattern, 0, len(categories))
	for _, c := range categories {
		for _, bp := range builtinPIIPatterns {
			if bp.category == c {
				patterns = append(patterns, piiPattern{regex: bp.regex, label: bp.label})
			}
		}
	}
	piiPatternsMu.Lock()
	piiPatterns = patterns
	piiPatternsMu.Unlock()
}

func getPIIPatterns() []piiPattern {
	piiPatternsMu.RLock()
	defer piiPatternsMu.RUnlock()
	return piiPatterns
}

// Pre-compiled builtin PII regexes.
var (
	emailRegex = regexp.MustCompile(`\b[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}\b`)
	// phoneRegex uses three branches to avoid false-positives on dotted-decimal
	// strings like version numbers (1.234.567.8901) and IPs (192.168.001.0001).
	// Dots are only allowed as separators when preceded by +1 (unambiguous intl prefix).
	// Without +1, only dashes and spaces are accepted as separators.
	phoneRegex = regexp.MustCompile(
		`(?:` +
			`\+1[-.\s]?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}` + // +1 intl prefix: any separator
			`|` +
			`(?:1[-\s])?\(\d{3}\)\s?\d{3}[-.\s]?\d{4}` + // parenthesized area code
			`|` +
			`(?:1[-\s])?\d{3}[-\s]\d{3}[-\s]\d{4}` + // bare digits: dash/space only
			`)`,
	)
)

// emailAllowPatterns are email patterns that should NOT be treated as PII.
// These appear frequently in coding transcripts (git authors, bot accounts)
// and are public metadata rather than private information.
// Entries starting with "@" match the email suffix; entries ending with "@"
// match the email prefix. All comparisons are case-insensitive.
var emailAllowPatterns = []string{
	"noreply@",                  // Generic noreply addresses
	"actions@",                  // GitHub Actions bot
	"@users.noreply.github.com", // GitHub user noreply
	"@noreply.github.com",       // GitHub noreply
}

// isAllowlistedEmail returns true if the email matches a known non-sensitive pattern.
func isAllowlistedEmail(email string) bool {
	lower := strings.ToLower(email)
	for _, pattern := range emailAllowPatterns {
		lp := strings.ToLower(pattern)
		switch {
		case strings.HasPrefix(pattern, "@"):
			if strings.HasSuffix(lower, lp) {
				return true
			}
		case strings.HasSuffix(pattern, "@"):
			if strings.HasPrefix(lower, lp) {
				return true
			}
		default:
			if lower == lp {
				return true
			}
		}
	}
	return false
}

// builtinPIIPattern associates a compiled regex with a category and label.
type builtinPIIPattern struct {
	category PIICategory
	label    string
	regex    *regexp.Regexp
}

// builtinPIIPatterns is the set of PII detection patterns.
var builtinPIIPatterns = []builtinPIIPattern{
	{PIIEmail, labelEmail, emailRegex},
	{PIIPhone, labelPhone, phoneRegex},
}

// detectPII returns tagged regions for PII matches in s. Returns nil
// immediately when no categories are configured.
func detectPII(patterns []piiPattern, s string) []taggedRegion {
	var regions []taggedRegion
	for _, p := range patterns {
		for _, loc := range p.regex.FindAllStringIndex(s, -1) {
			// Skip allowlisted email addresses (noreply, bot accounts, etc.).
			if p.label == labelEmail && isAllowlistedEmail(s[loc[0]:loc[1]]) {
				continue
			}
			regions = append(regions, taggedRegion{
				region: region{loc[0], loc[1]},
				label:  p.label,
			})
		}
	}
	return regions
}

// replacementToken returns the redaction placeholder for a given label.
// Empty label (secrets) returns "REDACTED" for backward compatibility.
// Non-empty label (PII) returns "[REDACTED_<LABEL>]".
func replacementToken(label string) string {
	if label == "" {
		return "REDACTED"
	}
	return "[REDACTED_" + label + "]"
}
