// Package redact masks secrets and PII in captured data on the user's
// own machine, before anything is uploaded: unredacted data never
// leaves it. Detection is layered pattern matching over text; for JSON
// input the replacements are applied field-aware so masking never
// breaks JSON structure, message order, tool_use pairing, or thinking
// signatures, and unparseable input degrades to a full-text scan rather
// than going out unmasked.
package redact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/betterleaks/betterleaks/detect"
)

// secretPattern matches high-entropy strings that may be secrets.
// Note: / is excluded to prevent matching entire file paths as single tokens.
// Base64 and JWT tokens are still caught via high-entropy segments between slashes.
var secretPattern = regexp.MustCompile(`[A-Za-z0-9+_=-]{10,}`)

// credentialedURIPattern matches URLs that embed userinfo with a password, such
// as postgres://user:pass@host/db or redis://:pass@host/0. These often have
// moderate entropy and are not reliably covered by vendor-specific scanners.
var credentialedURIPattern = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]{1,31}://[^\s/?#@"'` + "`" + `<>:]*:[^\s/?#@"'` + "`" + `<>]+@[^\s"'` + "`" + `<>]+`)

// dbPasswordKeyShape matches a DB-prefixed credential key (vendor prefix +
// optional `_word`/`-word` segments + `password`/`passwd`/`pwd`). Used to
// compose both the env-var assignment regex and the JSON-key regex so the
// vendor list stays in one place.
const dbPasswordKeyShape = `(?:db|database|pg|postgres|postgresql|mysql|mariadb|redis|mongo|mongodb|sqlserver|mssql|jdbc)(?:[_-]+[a-z0-9]+)*[_-]*(?:password|passwd|pwd)` //nolint:gosec // regex literal, not a credential

var (
	jdbcPattern          = regexp.MustCompile(`(?i)\bjdbc:[^\s"'<>` + "`" + `]+`)
	databaseURLPattern   = regexp.MustCompile(`(?i)\b(?:postgres(?:ql)?|mysql|mariadb|mongodb(?:\+srv)?|redis)://[^\s"'<>` + "`" + `]+`)
	keywordDSNPattern    = regexp.MustCompile(`(?i)\b[a-z_][a-z0-9_]*=(?:"[^"]*"|'[^']*'|[^\s"']+)(?:\s+[a-z_][a-z0-9_]*=(?:"[^"]*"|'[^']*'|[^\s"']+)){2,}`)
	semicolonConnPattern = regexp.MustCompile(`(?i)\b[a-z][a-z0-9 _-]*=(?:\{[^}]*\}|"[^"]*"|'[^']*'|[^=;"'\s]+)(?:;[a-z][a-z0-9 _-]*=(?:\{[^}]*\}|"[^"]*"|'[^']*'|[^=;"'\s]+)){2,}`)
	// credentialValuePattern requires the prefix to start at a non-alphanumeric
	// boundary, so APP_DB_PASSWORD matches via the leading `_` but mydbpassword
	// does not.
	credentialValuePattern = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])(` + dbPasswordKeyShape + `)\s*=\s*("[^"]*"|'[^']*'|[^\s,;&]+)`)

	keywordHostPattern      = regexp.MustCompile(`(?i)(?:^|\s)host=`)
	keywordUserPattern      = regexp.MustCompile(`(?i)(?:^|\s)user=`)
	semicolonServerPattern  = regexp.MustCompile(`(?i)(?:^|;)\s*(?:server|data source|datasource|addr|address|network address)\s*=`)
	semicolonUserPattern    = regexp.MustCompile(`(?i)(?:^|;)\s*(?:user id|userid|user|uid)\s*=`)
	passwordAssignmentRegex = regexp.MustCompile(`(?i)(?:^|[?&;\s])(?:password|pwd)=("[^"]*"|'[^']*'|[^&;\s"']+)`)
	// credentialJSONKeyRegex operates on output of normalizeCredentialJSONKey
	// (already lowercased, `-`/` `/`.` → `_`), so the `(?i)` flag is unnecessary.
	credentialJSONKeyRegex  = regexp.MustCompile(`^` + dbPasswordKeyShape + `$`)
	genericPasswordKeyRegex = regexp.MustCompile(`(?i)^(?:password|passwd|pwd)$`)
)

// entropyThreshold is the minimum Shannon entropy for a string to be considered
// a secret. 4.5 was chosen through trial and error: high enough to avoid false
// positives on common words and identifiers, low enough to catch typical API keys
// and tokens which tend to have entropy well above 5.0.
const entropyThreshold = 4.5

// redactedPlaceholder is the replacement text used for redacted secrets.
const redactedPlaceholder = "REDACTED"

// placeholderSecretValues lists lowercase values that should be treated as
// non-secrets when they appear as a credential value: prior redactions
// (REDACTED / [REDACTED] / <REDACTED>), common documentation placeholders,
// and obviously-non-real defaults. Values matched by shape (mask runs,
// `<…>` brackets, `${…}` shell expansion) are handled separately.
var placeholderSecretValues = func() map[string]struct{} {
	lower := strings.ToLower(redactedPlaceholder)
	values := []string{
		lower, "[" + lower + "]", "<" + lower + ">",
		"changeme", "example", "placeholder",
		"your_password", "your_db_password", "your_secret",
		"secret_here",
	}
	out := make(map[string]struct{}, len(values))
	for _, v := range values {
		out[v] = struct{}{}
	}
	return out
}()

// RedactedBytes represents data that has been through secret redaction.
// Consumers that require pre-redacted input accept this type to enforce
// the contract at compile time: unredacted data must never leave the
// machine, and the type is how a compiler proves a path cannot ship it.
//
// Produced by JSONLBytes (primary constructor) or AlreadyRedacted for
// trusted sources.
type RedactedBytes struct {
	data []byte
}

// Bytes returns the underlying byte slice.
func (r RedactedBytes) Bytes() []byte {
	return r.data
}

// Len returns the number of bytes in the redacted payload.
func (r RedactedBytes) Len() int {
	return len(r.data)
}

// AlreadyRedacted wraps bytes known to already be redacted: output
// derived mechanically from RedactedBytes (such as the compressed
// record stream a batch carries) or controlled test fixtures. For fresh
// input, use JSONLBytes.
func AlreadyRedacted(data []byte) RedactedBytes {
	return RedactedBytes{data: data}
}

var (
	betterleaksDetector     *detect.Detector
	betterleaksDetectorOnce sync.Once
)

func getDetector() *detect.Detector {
	betterleaksDetectorOnce.Do(func() {
		d, err := detect.NewDetectorDefaultConfig()
		if err != nil {
			return
		}
		betterleaksDetector = d
	})
	return betterleaksDetector
}

// region represents a byte range to redact.
type region struct{ start, end int }

// taggedRegion extends region with a label for typed replacement tokens.
// Empty label = secret (produces "REDACTED"). Non-empty = PII (produces "[REDACTED_<LABEL>]").
type taggedRegion struct {
	region

	label string
}

type jsonReplacement struct {
	key      string
	original string
	redacted string
}

type connectionStringRule struct {
	pattern   *regexp.Regexp
	hasSecret func(string) bool
}

var connectionStringRules = []connectionStringRule{
	{pattern: jdbcPattern, hasSecret: hasJDBCPassword},
	{pattern: databaseURLPattern, hasSecret: hasDatabaseURLSecret},
	{pattern: keywordDSNPattern, hasSecret: hasKeywordDSNPassword},
	{pattern: semicolonConnPattern, hasSecret: hasSemicolonConnectionPassword},
}

// redactString replaces secrets and PII in s using layered detection:
//  1. Entropy-based: high-entropy alphanumeric sequences (threshold 4.5)
//  2. Pattern-based: betterleaks regex rules (260+ known secret formats)
//  3. Provider token prefixes: deterministic prefix rules for credential
//     formats betterleaks misses in isolation (e.g. Supabase sb_secret_)
//  4. Credentialed URIs: URLs containing userinfo passwords
//  5. Database connection strings: JDBC, keyword DSNs, and semicolon strings
//  6. Bounded credential key/value pairs: DB_PASSWORD=...
//  7. PII detection: email and phone patterns (only when configured via ConfigurePII)
//
// A string is redacted if ANY method flags it.
func redactString(s string) string {
	return applyRegions(s, detectAllLayers(s))
}

// detectAllLayers runs the seven always-on/opt-in regex-based redaction
// layers and returns their tagged regions.
func detectAllLayers(s string) []taggedRegion {
	var regions []taggedRegion

	// 1. Entropy-based detection (secrets — always on).
	for _, loc := range secretPattern.FindAllStringIndex(s, -1) {
		start, end := loc[0], loc[1]

		// Don't consume characters that are part of JSON/string escape sequences.
		// Example: in "controller.go\nmodel.go", the regex could match "nmodel"
		// (consuming the 'n' from '\n'), and after replacement the '\' would be
		// followed by 'R' from "REDACTED", creating invalid escape '\R'.
		// Only skip for known JSON escape letters to avoid trimming real secrets
		// that happen to follow a literal backslash in decoded content.
		if start > 0 && s[start-1] == '\\' {
			switch s[start] {
			case 'n', 't', 'r', 'b', 'f', 'u', '"', '\\', '/':
				start++
				if end-start < 10 {
					continue
				}
			}
		}

		if shannonEntropy(s[start:end]) > entropyThreshold {
			regions = append(regions, taggedRegion{region: region{start, end}})
		}
	}

	// 2. Pattern-based detection via betterleaks (secrets — always on).
	if d := getDetector(); d != nil {
		for _, f := range d.DetectString(s) {
			if f.Secret == "" {
				continue
			}
			searchFrom := 0
			for {
				idx := strings.Index(s[searchFrom:], f.Secret)
				if idx < 0 {
					break
				}
				absIdx := searchFrom + idx
				regions = append(regions, taggedRegion{region: region{absIdx, absIdx + len(f.Secret)}})
				searchFrom = absIdx + len(f.Secret)
			}
		}
	}

	// 3. Provider-specific deterministic token prefixes (secrets — always on).
	// Catches low-entropy credential formats (e.g. Supabase sb_secret_) that
	// the entropy and betterleaks layers miss when captured in isolation.
	regions = append(regions, detectProviderTokens(s)...)

	// 4. Credentialed URIs (secrets — always on).
	for _, loc := range credentialedURIPattern.FindAllStringIndex(s, -1) {
		regions = append(regions, taggedRegion{region: region{loc[0], loc[1]}})
	}

	// 5. Database and connection-string detection (secrets — always on).
	regions = append(regions, detectConnectionStrings(s)...)

	// 6. Bounded credential key/value detection (secrets — always on).
	regions = append(regions, detectCredentialValues(s)...)

	// 7. PII detection (opt-in — only runs when configured).
	regions = append(regions, detectPII(getPIIPatterns(), s)...)

	return regions
}

// applyRegions sorts, merges, and replaces the given regions in s, returning
// the redacted string. Returns s unchanged when regions is empty.
func applyRegions(s string, regions []taggedRegion) string {
	if len(regions) == 0 {
		return s
	}

	sort.Slice(regions, func(i, j int) bool {
		if regions[i].start != regions[j].start {
			return regions[i].start < regions[j].start
		}
		if regions[i].end != regions[j].end {
			return regions[i].end > regions[j].end // larger region first
		}
		return regions[i].label < regions[j].label // deterministic tie-break
	})
	merged := []taggedRegion{regions[0]}
	for _, r := range regions[1:] {
		last := &merged[len(merged)-1]
		if r.start <= last.end {
			if r.end > last.end {
				last.end = r.end
			}
			// Keep the existing label (first/larger region wins)
		} else {
			merged = append(merged, r)
		}
	}

	var b strings.Builder
	prev := 0
	for _, r := range merged {
		b.WriteString(s[prev:r.start])
		b.WriteString(replacementToken(r.label))
		prev = r.end
	}
	b.WriteString(s[prev:])
	return b.String()
}

func detectConnectionStrings(s string) []taggedRegion {
	if !strings.ContainsRune(s, '=') {
		return nil
	}
	var regions []taggedRegion
	for _, rule := range connectionStringRules {
		regions = append(regions, detectConnectionStringRule(s, rule)...)
	}
	return regions
}

func detectConnectionStringRule(s string, rule connectionStringRule) []taggedRegion {
	var regions []taggedRegion
	for _, loc := range rule.pattern.FindAllStringIndex(s, -1) {
		start, end := loc[0], trimConnectionStringEnd(s, loc[0], loc[1])
		if start >= end {
			continue
		}
		if rule.hasSecret(s[start:end]) {
			regions = append(regions, taggedRegion{region: region{start, end}})
		}
	}
	return regions
}

func trimConnectionStringEnd(s string, start, end int) int {
	for end > start {
		switch s[end-1] {
		case '.', ',', ';', ':', '!', '?', ')', ']':
			end--
		default:
			return end
		}
	}
	return end
}

func hasJDBCPassword(candidate string) bool {
	if !strings.HasPrefix(strings.ToLower(candidate), "jdbc:") {
		return false
	}
	return hasNonPlaceholderPasswordAssignment(candidate)
}

// hasDatabaseURLSecret reports whether a database URL carries a password
// in its query string. databaseURLPattern already settled the scheme.
//
// The search runs on the raw text through the same assignment regex the
// JDBC and keyword-DSN rules use. It went through url.Parse and Query
// until 2026-08-14, and Query silently drops any pair whose value holds
// an invalid percent escape: a password containing a bare '%' made this
// answer "no password here" and left the whole URL unmasked. Redaction
// must not fail open on a parsing quirk.
func hasDatabaseURLSecret(candidate string) bool {
	return hasNonPlaceholderPasswordAssignment(candidate)
}

func hasKeywordDSNPassword(candidate string) bool {
	return keywordHostPattern.MatchString(candidate) &&
		keywordUserPattern.MatchString(candidate) &&
		hasNonPlaceholderPasswordAssignment(candidate)
}

func hasSemicolonConnectionPassword(candidate string) bool {
	return semicolonServerPattern.MatchString(candidate) &&
		semicolonUserPattern.MatchString(candidate) &&
		hasNonPlaceholderPasswordAssignment(candidate)
}

func detectCredentialValues(s string) []taggedRegion {
	var regions []taggedRegion
	for _, loc := range credentialValuePattern.FindAllStringSubmatchIndex(s, -1) {
		if len(loc) < 6 || loc[4] < 0 || loc[5] < 0 {
			continue
		}
		start, end := unquoteRange(s, loc[4], loc[5])
		if hasNonPlaceholderPasswordValue(s[start:end]) {
			regions = append(regions, taggedRegion{region: region{start, end}})
		}
	}
	return regions
}

func unquoteRange(s string, start, end int) (int, int) {
	if end-start < 2 {
		return start, end
	}
	first, last := s[start], s[end-1]
	if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
		return start + 1, end - 1
	}
	return start, end
}

func hasNonPlaceholderPasswordAssignment(candidate string) bool {
	for _, loc := range passwordAssignmentRegex.FindAllStringSubmatchIndex(candidate, -1) {
		if len(loc) >= 4 && loc[2] >= 0 && loc[3] >= 0 {
			start, end := unquoteRange(candidate, loc[2], loc[3])
			if hasNonPlaceholderPasswordValue(candidate[start:end]) {
				return true
			}
		}
	}
	return false
}

func hasNonPlaceholderPasswordValue(value string) bool {
	return value != "" && !isPlaceholderSecretValue(value)
}

func isPlaceholderSecretValue(value string) bool {
	trimmed := strings.Trim(strings.TrimSpace(value), `"'`)
	if trimmed == "" {
		return true
	}
	if isBracketedPlaceholder(trimmed) {
		return true
	}
	normalized := strings.ToLower(trimmed)
	if strings.HasPrefix(normalized, "${") && strings.HasSuffix(normalized, "}") {
		return true
	}
	if _, ok := placeholderSecretValues[normalized]; ok {
		return true
	}
	return isRepeatedCharPlaceholder(normalized)
}

// bracketedPlaceholderInteriorRE matches the inside of a "<…>" placeholder
// shape: lowercase letters joined by `-` or `_`. Digits, mixed case, and
// special chars are rejected so values like `<hunter2>` or `<RealPassword>`
// still fall through to redaction.
var bracketedPlaceholderInteriorRE = regexp.MustCompile(`^[a-z][a-z_-]*$`)

// isBracketedPlaceholder reports whether s is a "<name>" doc placeholder
// (e.g. "<password>", "<your-db-password>"). The minimum total length of 5
// keeps this from firing on `<a>` / `<ab>`.
func isBracketedPlaceholder(s string) bool {
	if len(s) < 5 || s[0] != '<' || s[len(s)-1] != '>' {
		return false
	}
	return bracketedPlaceholderInteriorRE.MatchString(s[1 : len(s)-1])
}

// isRepeatedCharPlaceholder reports whether s is a run of a single masking
// character commonly used to redact values in docs and screenshots, e.g.
// "***", "xxxx", "....", "----". The minimum length of 3 keeps single-char
// or 2-char values like `x` or `**` from being treated as masks.
func isRepeatedCharPlaceholder(s string) bool {
	if len(s) < 3 {
		return false
	}
	first := s[0]
	switch first {
	case '*', 'x', '.', '-':
	default:
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] != first {
			return false
		}
	}
	return true
}

func isCredentialJSONSecretKey(key string, credentialContext bool) bool {
	normalized := normalizeCredentialJSONKey(key)
	if credentialJSONKeyRegex.MatchString(normalized) {
		return true
	}
	return credentialContext && genericPasswordKeyRegex.MatchString(normalized)
}

func isCredentialJSONObject(obj map[string]any) bool {
	var hasHost, hasUser bool
	for key := range obj {
		switch normalizeCredentialJSONKey(key) {
		case "host", "hostname", "server", "addr", "address", "datasource", "data_source":
			hasHost = true
		case "user", "username", "userid", "user_id", "uid":
			hasUser = true
		}
		if hasHost && hasUser {
			return true
		}
	}
	return false
}

func normalizeCredentialJSONKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "-", "_")
	key = strings.ReplaceAll(key, " ", "_")
	// Flattened-config exporters (Spring, dotnet, Hashicorp Vault) emit dotted keys
	// like "db.password" or "mysql.root.password"; treat them like underscored.
	key = strings.ReplaceAll(key, ".", "_")
	return key
}

// JSONLBytes redacts secrets in JSONL-formatted byte content and returns
// the result as RedactedBytes, certifying the output has been through redaction.
func JSONLBytes(b []byte) (RedactedBytes, error) {
	s := string(b)
	redacted, err := jsonlContent(s)
	if err != nil {
		return RedactedBytes{}, err
	}
	if redacted == s {
		return RedactedBytes{data: b}, nil
	}
	return RedactedBytes{data: []byte(redacted)}, nil
}

// jsonlContent parses each line as JSON to determine which string values
// need redaction, then performs targeted replacements on the raw JSON bytes.
// Lines with no secrets are returned unchanged, preserving original formatting.
//
// For multi-line JSON content (e.g., pretty-printed single JSON objects like
// OpenCode export), the function first attempts to parse the entire content as
// a single JSON value. This ensures field-aware redaction (which skips ID fields)
// is used instead of falling back to entropy-based detection on raw text lines,
// which would corrupt high-entropy identifiers.
func jsonlContent(content string) (string, error) {
	// Try parsing the entire content as a single JSON value first.
	// Uses a streaming decoder to avoid copying the full content into []byte.
	// After decoding, attempts a second Decode to confirm EOF — if it succeeds,
	// the content is JSONL (multiple values) and we fall through to line-by-line.
	trimmed := strings.TrimSpace(content)
	if len(trimmed) > 0 {
		dec := json.NewDecoder(strings.NewReader(trimmed))
		var parsed any
		if err := dec.Decode(&parsed); err == nil && isSingleJSONValue(dec) {
			// Content is a single JSON value (object/array) — redact field-aware.
			result, err := applyJSONReplacements(content, collectJSONLReplacements(parsed))
			if err != nil {
				return "", err
			}
			return result, nil
		}
	}

	// Fall back to line-by-line JSONL processing.
	lines := strings.Split(content, "\n")
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		lineTrimmed := strings.TrimSpace(line)
		if lineTrimmed == "" {
			b.WriteString(line)
			continue
		}
		var parsed any
		if err := json.Unmarshal([]byte(lineTrimmed), &parsed); err != nil {
			b.WriteString(redactString(line))
			continue
		}
		result, err := applyJSONReplacements(line, collectJSONLReplacements(parsed))
		if err != nil {
			return "", err
		}
		b.WriteString(result)
	}
	return b.String(), nil
}

// applyJSONReplacements applies collected (original, redacted) string pairs
// to the raw JSON text, replacing the string tokens whose *decoded* value
// matches a collected original. Returns s unchanged if repls is empty or if
// nothing matched, preserving the original formatting byte for byte.
//
// Matching must be on the decoded value, never on a re-encoded form of the
// original: collectJSONLReplacements works on values decoded by
// encoding/json, while the raw text is free to spell any of them differently
// — `—` for an em dash, `\/` for a slash, upper- or lower-case hex. A byte
// search for one spelling silently misses the others, and a silent miss here
// is a secret leaving the machine unmasked.
func applyJSONReplacements(s string, repls []jsonReplacement) (string, error) {
	if len(repls) == 0 {
		return s, nil
	}

	// keyed replacements apply only to the value of that exact key; unkeyed
	// ones apply to any string token, which is what the previous ReplaceAll
	// did and what array elements (collected with an empty key) rely on.
	keyed := make(map[string]map[string]string)
	unkeyed := make(map[string]string, len(repls))
	for _, r := range repls {
		replJSON, err := jsonEncodeString(r.redacted)
		if err != nil {
			return "", err
		}
		if r.key == "" {
			unkeyed[r.original] = replJSON
			continue
		}
		byValue := keyed[r.key]
		if byValue == nil {
			byValue = make(map[string]string)
			keyed[r.key] = byValue
		}
		byValue[r.original] = replJSON
	}

	// The enclosing containers. The top of the stack tells a value token which
	// key owns it; a token directly inside an array has no owning key, which is
	// how array elements stay restricted to the unkeyed replacements.
	//
	// The skip policy is enforced here, on the applying side, so that it holds
	// for every replacement regardless of where it was collected: a protected
	// field keeps its observed value even when an unkeyed (array-element)
	// replacement collides with it. keySkipped marks the value owned by the
	// current key; skipped marks a container living anywhere inside a
	// protected field's subtree.
	type frame struct {
		isObject   bool
		skipped    bool
		keySkipped bool
		key        string
	}
	var stack []frame
	enteringSkipped := func() bool {
		if len(stack) == 0 {
			return false
		}
		top := stack[len(stack)-1]
		return top.skipped || top.keySkipped
	}
	// A protected key skips its scalar value and an array it owns (an ids
	// array keeps its elements). An object value, though, re-enters normal
	// evaluation: its fields carry their own names, so each is judged on its
	// own key rather than blanket-skipped by an ancestor id/signature/path
	// key. Only a genuine container-wide skip reaches into a nested object.
	enteringSkippedObject := func() bool {
		if len(stack) == 0 {
			return false
		}
		return stack[len(stack)-1].skipped
	}

	var b strings.Builder
	i, written := 0, 0
	for i < len(s) {
		switch s[i] {
		case '{':
			stack = append(stack, frame{isObject: true, skipped: enteringSkippedObject()})
			i++
		case '[':
			stack = append(stack, frame{skipped: enteringSkipped()})
			i++
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			i++
		case '"':
			end, ok := jsonStringEnd(s, i)
			if !ok {
				// Unterminated string: the text is not the valid JSON the
				// caller parsed. Leave the remainder alone rather than risk
				// mangling it.
				i = len(s)
				continue
			}
			value, decoded := decodeJSONStringToken(s[i:end])
			// In valid JSON only an object key can be followed by ':'.
			isKey := false
			if p := skipJSONWhitespace(s, end); p < len(s) && s[p] == ':' {
				isKey = true
			}
			inObject := len(stack) > 0 && stack[len(stack)-1].isObject
			if isKey && inObject && decoded {
				stack[len(stack)-1].key = value
				stack[len(stack)-1].keySkipped = shouldSkipJSONLField(value)
			}
			// Keys are structure, never rewritten; values in skipped
			// territory keep their observed bytes.
			if decoded && !isKey && !enteringSkipped() {
				replJSON, found := "", false
				if inObject {
					replJSON, found = keyed[stack[len(stack)-1].key][value]
				}
				if !found {
					replJSON, found = unkeyed[value]
				}
				if found {
					if written == 0 {
						b.Grow(len(s))
					}
					b.WriteString(s[written:i])
					b.WriteString(replJSON)
					written = end
				}
			}
			i = end
		default:
			i++
		}
	}
	if written == 0 {
		return s, nil
	}
	b.WriteString(s[written:])
	return b.String(), nil
}

// jsonStringEnd returns the index just past the closing quote of the JSON
// string token that starts at s[start], which must be '"'.
func jsonStringEnd(s string, start int) (int, bool) {
	for i := start + 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++ // skip the escaped byte; the loop's i++ steps past it
		case '"':
			return i + 1, true
		}
	}
	return 0, false
}

// decodeJSONStringToken decodes a complete JSON string token (quotes included)
// to its Go string value. Tokens without escapes — the overwhelming majority —
// take a substring instead of a round trip through encoding/json.
func decodeJSONStringToken(tok string) (string, bool) {
	if len(tok) < 2 {
		return "", false
	}
	if strings.IndexByte(tok, '\\') < 0 {
		return tok[1 : len(tok)-1], true
	}
	var v string
	if err := json.Unmarshal([]byte(tok), &v); err != nil {
		return "", false
	}
	return v, true
}

func skipJSONWhitespace(s string, i int) int {
	for i < len(s) && isJSONWhitespace(s[i]) {
		i++
	}
	return i
}

func isJSONWhitespace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// isSingleJSONValue returns true if the decoder has reached EOF (no more
// top-level values). This distinguishes a single JSON value (e.g., pretty-printed
// object) from JSONL (multiple concatenated values). We attempt a second Decode
// and require io.EOF rather than relying on dec.More(), which is documented for
// use inside arrays/objects and not for top-level value boundaries.
func isSingleJSONValue(dec *json.Decoder) bool {
	var discard json.RawMessage
	return dec.Decode(&discard) == io.EOF
}

// collectJSONLReplacements walks a parsed JSON value and collects unique
// string replacements. Protected fields are not excluded here: the skip
// policy lives in applyJSONReplacements, the one place that decides what
// gets rewritten.
func collectJSONLReplacements(v any) []jsonReplacement {
	seen := make(map[string]bool)
	var repls []jsonReplacement
	var walk func(key string, credentialContext bool, v any)
	walk = func(key string, credentialContext bool, v any) {
		switch val := v.(type) {
		case map[string]any:
			// An image/base64 object carries a binary payload in its data or
			// url field: that value is preserved verbatim, but the object's
			// other fields (captions, ids, arbitrary siblings) are still text
			// and are scanned normally.
			skipPayload := shouldSkipJSONLObject(val)
			childCredentialContext := credentialContext || isCredentialJSONObject(val)
			for k, child := range val {
				if skipPayload && (k == "data" || k == "url") {
					continue
				}
				walk(k, childCredentialContext, child)
			}
		case []any:
			for _, child := range val {
				walk("", credentialContext, child)
			}
		case string:
			redacted := redactString(val)
			// The key itself says this value is a password, so the whole
			// value goes — never just the parts the pattern layers caught.
			// Until 2026-08-14 this ran only when redactString had changed
			// nothing, which inverted the guarantee: a password holding one
			// recognizable token kept the rest of itself in the clear,
			// while one nothing recognized was masked entirely.
			if isCredentialJSONSecretKey(key, credentialContext) && hasNonPlaceholderPasswordValue(val) {
				redacted = redactedPlaceholder
			}
			if redacted != val {
				seenKey := key + "\x00" + val
				if !seen[seenKey] {
					seen[seenKey] = true
					repls = append(repls, jsonReplacement{key: key, original: val, redacted: redacted})
				}
			}
		}
	}
	walk("", false, v)
	return repls
}

// shouldSkipJSONLField returns true if the value (subtree included) owned by
// a JSON key must never be rewritten. Protects signature fields (any key
// ending in "signature"), ID fields (ending in "id"/"ids"), and common
// path/directory fields. Enforced by applyJSONReplacements.
func shouldSkipJSONLField(key string) bool {
	lower := strings.ToLower(key)

	// Skip signature fields: cryptographic attestations, not secrets. Covers
	// "signature" (Claude Code) and provider variants like "thinkingSignature".
	// Their values are high-entropy base64, so the entropy scanner would
	// otherwise redact them — corrupting extended-thinking signatures, which
	// must be preserved verbatim.
	if strings.HasSuffix(lower, "signature") {
		return true
	}

	// Skip ID fields
	if strings.HasSuffix(lower, "id") || strings.HasSuffix(lower, "ids") {
		return true
	}

	// Skip common path and directory fields from agent transcripts.
	// These appear frequently in tool calls and are structural, not secrets.
	switch lower {
	case "filepath", "file_path", "cwd", "root", "directory", "dir", "path":
		return true
	}

	return false
}

// shouldSkipJSONLObject reports whether the object carries a binary
// payload — "type":"image"/"image_url" or "type":"base64" — whose data or
// url field is preserved verbatim rather than scanned.
//
// The type is matched exactly. It was a prefix match on "image" until
// 2026-08-14, but "type" is free text a model or an MCP tool writes into
// a recorded body, so anything calling itself image_metadata carried its
// url and data past all seven detection layers.
func shouldSkipJSONLObject(obj map[string]any) bool {
	t, ok := obj["type"].(string)
	if !ok {
		return false
	}
	switch t {
	case "image", "image_url", "base64":
		return true
	}
	return false
}

func shannonEntropy(s string) float64 {
	if len(s) == 0 {
		return 0
	}
	freq := make(map[byte]int)
	for i := range len(s) {
		freq[s[i]]++
	}
	length := float64(len(s))
	var entropy float64
	for _, count := range freq {
		p := float64(count) / length
		entropy -= p * math.Log2(p)
	}
	return entropy
}

// jsonEncodeString returns the JSON encoding of s without HTML escaping.
func jsonEncodeString(s string) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return "", fmt.Errorf("json encode string: %w", err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}
