package redact

import (
	"regexp"
	"strings"
	"sync"
)

// piiCategory identifies a category of personally identifying strings
// the redaction pass can mask.
type piiCategory string

const (
	piiEmail piiCategory = "email"
	piiPhone piiCategory = "phone"
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

// defaultPIICategories are the categories every process masks without
// being told to. The redaction pass runs in more than one kind of
// process, and one of them has no startup step where a switch could be
// flipped; a default that depends on who narrows the categories first
// is a default that is off somewhere. Email and phone are the two
// shapes that can be matched without misfiring on code and prose;
// street addresses cannot.
var defaultPIICategories = []piiCategory{piiEmail, piiPhone}

var (
	piiPatterns   = piiPatternsFor(defaultPIICategories)
	piiPatternsMu sync.RWMutex
)

// configurePII narrows or widens which PII categories the redaction
// pass masks, replacing the default. Matches are replaced with
// [REDACTED_<CATEGORY>] tokens. Thread-safe; no process needs to call
// it to get the default.
func configurePII(categories ...piiCategory) {
	patterns := piiPatternsFor(categories)
	piiPatternsMu.Lock()
	piiPatterns = patterns
	piiPatternsMu.Unlock()
}

func piiPatternsFor(categories []piiCategory) []piiPattern {
	patterns := make([]piiPattern, 0, len(categories))
	for _, c := range categories {
		for _, bp := range builtinPIIPatterns {
			if bp.category == c {
				patterns = append(patterns, piiPattern{regex: bp.regex, label: bp.label})
			}
		}
	}
	return patterns
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
// These appear frequently in coding sessions (git authors, bot accounts)
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
	category piiCategory
	label    string
	regex    *regexp.Regexp
}

// builtinPIIPatterns is the set of PII detection patterns.
var builtinPIIPatterns = []builtinPIIPattern{
	{piiEmail, labelEmail, emailRegex},
	{piiPhone, labelPhone, phoneRegex},
}

// detectPII returns tagged regions for PII matches in s. Returns nil
// when patterns is empty.
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
