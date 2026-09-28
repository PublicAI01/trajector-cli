package redact

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
)

// This file recognizes a credential by what surrounds it rather than by
// its own format: a key that names a secret, a password beside a host
// someone logs in to, a password handed to a login tool. The format
// layers cannot see these. A 20-character hex API key or a 16-character
// password sits under the entropy threshold, and the generic key and
// password rules of the pattern layer are filtered out below medium
// confidence (see minRuleConfidence).
//
// Every judgement is stated once, as data in this file: which keys name
// a secret, what a secret value looks like, how far a host may sit from
// a password. The text scan and the JSON document walk both read these
// same tables, so one document gets one verdict however it is spelled.

// secretKeyClass is one family of keys that name a secret. Trailing
// digits and separators of a key are not part of its name (API_KEY_2 is
// API_KEY). A key then belongs to a class when its compact form
// (lowercased; `_`, `-`, `.` and spaces dropped) ends in one of the
// class's suffixes, compacted the same way; or is exactly one of its
// names; or ends in one of its words as a word of its own, after a
// separator or as a camelCase hump (signing_key, signingKey, but not
// monkey).
type secretKeyClass struct {
	suffixes []string
	names    []string
	words    []string
	// password marks the class of passwords. A password may be letters
	// alone, and a nearby host makes any value under it a credential
	// (see hostLoginWindowLines). The other classes hold generated
	// values, which mix letters with digits or symbols.
	password bool
	// minEntropy, when set, raises opaqueMinEntropy for this class.
	minEntropy float64
}

// secretKeyClasses is the list of keys that name a secret. A value under
// one of them is masked when it has the shape of a secret (isOpaqueValue);
// a value that reads as a word, a reference or a placeholder is left.
//
// Plurals are not here: max_tokens counts tokens and holds none. Nor is
// "pass": test runners print `PASS: <test name>`, so it counts only
// right after a login target (loginOnlyPasswordWords). The last class
// holds the generic words of the pattern layer's generic key rule,
// which minRuleConfidence filters out: the value shape does the work
// that rule's confidence rating did, and asks the entropy that rule
// asks (3.5): these words also name cache keys, sort keys and ids. AK
// and SK are the access key pair of several cloud consoles.
var secretKeyClasses = []secretKeyClass{
	{suffixes: []string{"password", "passwd", "pwd", "密码", "口令"}, names: []string{"pw"}, password: true},
	{suffixes: []string{"token"}},
	{suffixes: []string{"secret"}},
	{suffixes: []string{"api_key"}},
	{suffixes: []string{"access_key", "secret_key", "private_key"}},
	{words: []string{"key", "auth", "credential", "credentials", "creds"}, names: []string{"ak", "sk"}, minEntropy: 3.5},
}

// publicValuePrefixes start values that are designed to be published,
// such as a Supabase or Stripe publishable key (see providers.go). A
// key named like a secret does not make one of these a secret.
var publicValuePrefixes = []string{"sb_publishable_", "pk_live_", "pk_test_"}

// loginOnlyPasswordWords name a password only where a login target has
// already said what they are about: `root@10.0.0.5 pass hunter2`.
var loginOnlyPasswordWords = []string{"pass"}

// The shape of a secret value. A value is opaque when it is at least
// opaqueMinLength bytes of opaqueValueBytes, is not a placeholder or a
// reference, and has a run of opaqueMinLength bytes free of `_`, `-`,
// `.` and `/` (so snake_case, kebab-case, dotted and slashed names such
// as prod/db/password are not opaque). That run must hold letters and
// either digits or symbols as well; under a password key it may instead
// be letters that are not spelled as words (wordCasedPattern). The value
// as a whole must reach opaqueMinEntropy bits per byte.
//
// The entropy floor is low on purpose: the entropy layer already takes
// anything above 4.5, and a random 16-byte value cannot reach 4.5 at all
// (log2 16 = 4). What the floor rejects is repetition, as in
// hunter2hunter (2.8).
const (
	opaqueMinLength  = 8
	opaqueMinEntropy = 3.0
	opaqueValueBytes = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=_-.!@#%^&*"
)

// A login password is any value next to a login target that is at least
// loginMinLength bytes of printable ASCII, holds none of loginCodeBytes,
// and is not a placeholder or a reference. Code that builds a login
// (password: getenv("PW"), "-p '" + pw + "'", a; b; c) is the text these
// bytes keep out; a password that holds one of them is rare.
const (
	loginMinLength = 4
	loginCodeBytes = "()\"'`\\${};"
)

// hostLoginWindowLines is how many lines above or below a password key
// a host may sit and still make the key's value a login credential.
// A pasted login is a few lines: the ssh command, then the password; or
// host, user and password as three lines.
const hostLoginWindowLines = 3

var (
	// userHostPattern is a login target: user@host, where the host is an
	// IPv4 address, localhost or a dotted name.
	userHostPattern = `[A-Za-z0-9._%+\-]+@(?:\d{1,3}(?:\.\d{1,3}){3}|localhost|[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)+)`

	// hostIndicatorPattern is anything that names a machine to log in
	// to: a user@host target, a bare IPv4 address, or a line that
	// starts with a host key.
	hostIndicatorPattern = regexp.MustCompile(`(?im)` + userHostPattern + `|\b\d{1,3}(?:\.\d{1,3}){3}\b|^[ \t]*(?:host|hostname|server|主机|服务器)[ \t]*[=:：]`)

	// inlineLoginPattern is a login target, an optional port, a password
	// word and the password, as prose writes it: `root@10.0.0.5:2222 pass
	// hunter2`. The separator is optional here because the target
	// already says what the word is about, but the word must end at a
	// separator or a space: passwordless and passphrase are other words.
	inlineLoginPattern = regexp.MustCompile(`(?i)` + userHostPattern + `(?::\d{1,5})?[ \t]+(?:` + passwordWordAlternation() + `)(?:[ \t]*[=:：][ \t]*|[ \t]+)("[^"]*"|'[^']*'|[!-~]+)`)

	// trailingLoginPattern is a login target with a port and one more
	// token that ends the line: `root@10.0.0.5:2222 Xk7qmQ2vR9`. Only an
	// opaque token counts; a word there is prose.
	trailingLoginPattern = regexp.MustCompile(`(?im)` + userHostPattern + `:\d{1,5}[ \t]+([!-~]+)[ \t\r]*$`)

	// sshpassPattern is the password argument of sshpass.
	sshpassPattern = regexp.MustCompile(`\bsshpass(?:[ \t]+-[A-Za-oq-z]\S*)*[ \t]+-p[ \t]*("[^"]*"|'[^']*'|[!-~]+)`)

	// dottedReferencePattern is a member access such as
	// process.env.API_KEY or self.token: a name of a value, not a value.
	dottedReferencePattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*(?:\.[A-Za-z_$][A-Za-z0-9_$]*)+$`)

	// wordCasedPattern is letters spelled as words: lower, UPPER,
	// Capitalized, camelCase or PascalCase.
	wordCasedPattern = regexp.MustCompile(`^(?:[a-z]+|[A-Z]+|[A-Z]?[a-z]+(?:[A-Z][a-z]+)*|[a-z]+(?:[A-Z][a-z]+)+)$`)
)

// keyAnchorWords is the last word of every key in secretKeyClasses. A
// key can name a secret only if it ends in one of them, which lets
// findKeyedValues pass over most keys without classifying them; the
// table stays the one list of keys.
var keyAnchorWords = func() []string {
	var terms []string
	for _, class := range secretKeyClasses {
		terms = append(terms, class.suffixes...)
		terms = append(terms, class.names...)
		terms = append(terms, class.words...)
	}
	seen := map[string]bool{}
	var words []string
	for _, term := range terms {
		word := term[strings.LastIndexByte(term, '_')+1:]
		if !seen[word] {
			seen[word] = true
			words = append(words, word)
		}
	}
	return words
}()

// nonASCIIKeyEndings are the terms of secretKeyClasses spelled outside
// ASCII. keyBefore reads one of them as the end of a key, since the key
// bytes it reads back over are ASCII.
var nonASCIIKeyEndings = func() []string {
	var endings []string
	for _, class := range secretKeyClasses {
		for _, term := range slices.Concat(class.suffixes, class.names, class.words) {
			if strings.ContainsFunc(term, func(r rune) bool { return r > '~' }) {
				endings = append(endings, term)
			}
		}
	}
	return endings
}()

// passwordWordAlternation is every word that names a password after a
// login target: the password class of secretKeyClasses and
// loginOnlyPasswordWords, as a regexp alternation.
func passwordWordAlternation() string {
	var words []string
	for _, class := range secretKeyClasses {
		if class.password {
			words = append(words, class.suffixes...)
			words = append(words, class.names...)
		}
	}
	words = append(words, loginOnlyPasswordWords...)
	for i, w := range words {
		words[i] = regexp.QuoteMeta(w)
	}
	return strings.Join(words, "|")
}

// secretKeyClassOf returns the class a key belongs to, or nil. The key
// may be spelled any way normalizeCredentialJSONKey accepts.
func secretKeyClassOf(key string) *secretKeyClass {
	key = strings.TrimRight(key, "0123456789_-. ")
	compact := compactKey(normalizeCredentialJSONKey(key))
	if compact == "" {
		return nil
	}
	for i := range secretKeyClasses {
		class := &secretKeyClasses[i]
		for _, n := range class.names {
			if compact == n {
				return class
			}
		}
		for _, s := range class.suffixes {
			if strings.HasSuffix(compact, compactKey(s)) {
				return class
			}
		}
		for _, w := range class.words {
			if endsWithWord(key, w) {
				return class
			}
		}
	}
	return nil
}

// endsWithWord reports whether key ends in word as a word of its own:
// the whole key, or after a separator, or as a camelCase hump.
func endsWithWord(key, word string) bool {
	i := len(key) - len(word)
	if i < 0 || !strings.EqualFold(key[i:], word) {
		return false
	}
	if i == 0 {
		return true
	}
	prev, first := key[i-1], key[i]
	return strings.IndexByte("_-. ", prev) >= 0 ||
		first >= 'A' && first <= 'Z' && (prev >= 'a' && prev <= 'z' || prev >= '0' && prev <= '9')
}

func compactKey(normalized string) string {
	return strings.ReplaceAll(normalized, "_", "")
}

// holdsSecret reports whether a value under a key of this class has the
// shape of a secret.
func (c *secretKeyClass) holdsSecret(value string) bool {
	return isOpaqueValue(value, c.password, cmp.Or(c.minEntropy, opaqueMinEntropy))
}

// isOpaqueValue reports whether a value has the shape of a secret. See
// the constants above for the rule; lettersAlone is set for a password.
func isOpaqueValue(value string, lettersAlone bool, minEntropy float64) bool {
	if len(value) < opaqueMinLength {
		return false
	}
	for i := range len(value) {
		if strings.IndexByte(opaqueValueBytes, value[i]) < 0 {
			return false
		}
	}
	if isPlaceholderSecretValue(value) || isShownMaskedOrCutShort(value) || isReferenceValue(value) {
		return false
	}
	core := longestUnseparatedRun(value)
	if len(core) < opaqueMinLength {
		return false
	}
	var letters, others bool
	for i := range len(core) {
		c := core[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			letters = true
		default:
			others = true
		}
	}
	if !letters || !others && (!lettersAlone || wordCasedPattern.MatchString(core)) {
		return false
	}
	return shannonEntropy(value) >= minEntropy
}

// isReferenceValue reports whether a value names where a secret is kept
// instead of being one: a path, a URL, a variable, a scoped package name
// or a member access. A value that is public by design counts too.
func isReferenceValue(value string) bool {
	switch value[0] {
	case '/', '.', '$', '%', '{', '<', '(', '[', '~', '@':
		return true
	}
	for _, p := range publicValuePrefixes {
		if strings.HasPrefix(value, p) {
			return true
		}
	}
	return strings.Contains(value, "://") || strings.HasPrefix(value, replacementToken("")) ||
		dottedReferencePattern.MatchString(value)
}

// longestUnseparatedRun returns the longest run of value that holds no
// `_`, `-`, `.` or `/`.
func longestUnseparatedRun(value string) string {
	best, start := "", 0
	for i := 0; i <= len(value); i++ {
		if i == len(value) || strings.IndexByte("_-./", value[i]) >= 0 {
			if i-start > len(best) {
				best = value[start:i]
			}
			start = i + 1
		}
	}
	return best
}

// isLoginSecretValue reports whether a value next to a login target is
// a password. See loginMinLength for the rule.
func isLoginSecretValue(value string) bool {
	if len(value) < loginMinLength || strings.ContainsAny(value, loginCodeBytes) {
		return false
	}
	for i := range len(value) {
		if value[i] <= ' ' || value[i] > '~' {
			return false
		}
	}
	return !isPlaceholderSecretValue(value) && !isShownMaskedOrCutShort(value) && !isReferenceValue(value)
}

// placeholderSecretMarks are the substrings of a key shown masked or cut
// short ("sk-****Ab12", "Ab12...Yz89"): what is left after something
// else hid the secret. A generated secret holds neither. A vendor's
// documented sample ("…EXAMPLE") is not one of them, for the reason
// given in providers.go: masking an example costs a few characters of
// it.
var placeholderSecretMarks = []string{"***", "..."}

// isShownMaskedOrCutShort reports whether a value holds one of
// placeholderSecretMarks. Only the verdicts that read a value by its
// shape (isOpaqueValue, isLoginSecretValue) ask it. A value under a
// database password key or in a connection string is masked unless it
// is a placeholder, and a value cut short there can still hold most of
// the password.
func isShownMaskedOrCutShort(value string) bool {
	return slices.ContainsFunc(placeholderSecretMarks, func(mark string) bool {
		return strings.Contains(value, mark)
	})
}

// namesOpaqueSecret is the one verdict that holds with no context at all:
// the key names a secret and the value has the shape of one. The JSON
// document walk asks it; detectKeyedCredentials asks the same two
// questions of a key and value it found in text.
func namesOpaqueSecret(key, value string) bool {
	class := secretKeyClassOf(key)
	return class != nil && class.holdsSecret(value)
}

// trimValueClosers drops the punctuation an unquoted value picks up from
// the text around it: a closing bracket or quote, a comma, a semicolon.
func trimValueClosers(value string) string {
	return strings.TrimRight(value, "`\"')]}>,;")
}

// keyedValue is one key, separator and value findKeyedValues found.
// The value spans valueStart to valueEnd; rawEnd is where it ran before
// boundedValueEnd cut off the assignments that follow it.
type keyedValue struct {
	class                *secretKeyClass
	keyStart             int
	keyQuoted            bool
	valueStart, valueEnd int
	rawEnd               int
}

// findKeyedValues finds each key of secretKeyClasses followed by a
// separator and a value on the same line. The key is a run of ASCII
// letters, digits, `_`, `.` and `-`, or such a run ending in one of
// nonASCIIKeyEndings. Between the key and the separator there may be
// markdown bold, the quote of a quoted key and the bracket of an index
// expression, in that order, then spaces. The separator is `=`, `:` or
// `：`, or a comma between the two quoted arguments of a call such as
// setenv("API_KEY", "…"); a comma in a JSON array separates elements,
// which have no key. After spaces, the value is a quoted string that
// closes on the same line, or else a run of printable ASCII up to the
// next assignment (boundedValueEnd). The scan goes on from where the
// value ends, so in api_key=…&client_secret=… both keys are read.
//
// It is a scan rather than a regexp because it runs over every string
// value twice: looking back from each separator is linear, where a
// pattern that must find where a key starts is not.
func findKeyedValues(s string) []keyedValue {
	var found []keyedValue
	for i := 0; i < len(s); i++ {
		sepLen := 0
		switch {
		case s[i] == '=' || s[i] == ':' || s[i] == ',':
			sepLen = 1
		case strings.HasPrefix(s[i:], "："):
			sepLen = len("：")
		default:
			continue
		}
		m, ok := keyBefore(s, i)
		if !ok || s[i] == ',' && !(m.keyQuoted && calledWith(s, m.keyStart)) {
			continue
		}
		start := i + sepLen
		for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
			start++
		}
		rawEnd := valueEnd(s, start)
		if rawEnd == start {
			continue
		}
		end := rawEnd
		if s[start] != '"' && s[start] != '\'' {
			end = boundedValueEnd(s, start, rawEnd)
		}
		m.valueStart, m.valueEnd, m.rawEnd = start, end, rawEnd
		found = append(found, m)
		i = m.valueEnd - 1
	}
	return found
}

// keyBefore reads back from the separator at sep to the key before it
// and classifies it.
func keyBefore(s string, sep int) (keyedValue, bool) {
	j := sep
	for j > 0 && (s[j-1] == ' ' || s[j-1] == '\t') {
		j--
	}
	if j > 0 && s[j-1] == ']' {
		j--
	}
	quoted := false
	if j > 0 && (s[j-1] == '"' || s[j-1] == '\'') {
		quoted = true
		j--
	}
	for j > 0 && s[j-1] == '*' {
		j--
	}
	keyEnd := j
	for _, ending := range nonASCIIKeyEndings {
		if strings.HasSuffix(s[:j], ending) {
			j -= len(ending)
			break
		}
	}
	for j > 0 && isKeyByte(s[j-1]) {
		j--
	}
	key := s[j:keyEnd]
	if key == "" || !endsInAnchorWord(key) {
		return keyedValue{}, false
	}
	class := secretKeyClassOf(key)
	if class == nil {
		return keyedValue{}, false
	}
	quoted = quoted || j > 0 && (s[j-1] == '"' || s[j-1] == '\'')
	return keyedValue{class: class, keyStart: j, keyQuoted: quoted}, true
}

func isKeyByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}

// endsInAnchorWord reports whether key, less any trailing number, ends
// in one of keyAnchorWords.
func endsInAnchorWord(key string) bool {
	key = strings.TrimRight(key, "0123456789_-.")
	for _, w := range keyAnchorWords {
		if len(key) >= len(w) && strings.EqualFold(key[len(key)-len(w):], w) {
			return true
		}
	}
	return false
}

// valueEnd returns where the value that starts at start ends: after
// the closing quote of a string that closes on the same line, or else
// after the run of printable ASCII.
func valueEnd(s string, start int) int {
	if start < len(s) && (s[start] == '"' || s[start] == '\'') {
		for k := start + 1; k < len(s) && s[k] != '\n'; k++ {
			if s[k] == s[start] {
				return k + 1
			}
		}
	}
	end := start
	for end < len(s) && s[end] > ' ' && s[end] <= '~' {
		end++
	}
	return end
}

// calledWith reports whether the quoted key that starts at keyStart is
// the first argument of a call: an opening parenthesis before its quote.
func calledWith(s string, keyStart int) bool {
	i := keyStart - 2
	for i >= 0 && (s[i] == ' ' || s[i] == '\t') {
		i--
	}
	return i >= 0 && s[i] == '('
}

// unquotedValue returns the span of a captured value without its
// quotes. A quoted value keeps every byte between them, except a pair
// of backticks, the markdown for code, around the whole of it. An
// unquoted value loses the closers it picked up from the text around
// it, and an opening quote with no closing one: that is the start of a
// value cut off at the end of a line, as a log or a diff shows one.
func unquotedValue(s string, start, end int) (int, int) {
	if qs, qe := unquoteRange(s, start, end); qs != start {
		if qe-qs >= 2 && s[qs] == '`' && s[qe-1] == '`' {
			return qs + 1, qe - 1
		}
		return qs, qe
	}
	if start < end && strings.IndexByte("`\"'", s[start]) >= 0 {
		start++
	}
	return start, start + len(trimValueClosers(s[start:end]))
}

// detectKeyedCredentials returns the regions of the keyed credentials
// and login passwords in s.
func detectKeyedCredentials(s string) []taggedRegion {
	var regions []taggedRegion
	var hosts *hostLines
	for _, m := range findKeyedValues(s) {
		keyStart := m.keyStart
		class := m.class
		keyQuoted := m.keyQuoted
		start, end := unquotedValue(s, m.valueStart, m.valueEnd)
		if class.holdsSecret(s[start:end]) {
			regions = append(regions, taggedRegion{region: region{start, end}})
			continue
		}
		// A quoted key is a JSON spelling, and the document walk owns the
		// context of JSON: it reads host and user keys, not nearby text.
		// A value that ends in `,` or `;` is an entry of a code literal.
		raw := s[m.valueStart:m.rawEnd]
		if !class.password || keyQuoted || strings.HasSuffix(raw, ",") || strings.HasSuffix(raw, ";") ||
			!isLoginSecretValue(s[start:end]) {
			continue
		}
		if hosts == nil {
			hosts = findHostLines(s)
		}
		if hosts.near(keyStart) {
			regions = append(regions, taggedRegion{region: region{start, end}})
		}
	}
	return regions
}

// detectLoginPasswords returns the regions of passwords written beside a
// login target or handed to sshpass.
func detectLoginPasswords(s string) []taggedRegion {
	var regions []taggedRegion
	add := func(pattern *regexp.Regexp, accept func(string) bool) {
		for _, loc := range pattern.FindAllStringSubmatchIndex(s, -1) {
			start, end := unquotedValue(s, loc[2], loc[3])
			if accept(s[start:end]) {
				regions = append(regions, taggedRegion{region: region{start, end}})
			}
		}
	}
	if strings.IndexByte(s, '@') >= 0 {
		add(inlineLoginPattern, isLoginSecretValue)
		add(trailingLoginPattern, func(v string) bool { return isOpaqueValue(v, true, opaqueMinEntropy) })
	}
	if strings.Contains(s, "sshpass") {
		add(sshpassPattern, isLoginSecretValue)
	}
	return regions
}

// hostLines holds the offsets of the line breaks of s and the numbers,
// in ascending order, of the lines that name a host. A value can hold
// a whole log, so a line number is found by a binary search over the
// breaks, never by counting them again.
type hostLines struct {
	breaks []int
	lines  []int
}

func findHostLines(s string) *hostLines {
	h := &hostLines{}
	for i := range len(s) {
		if s[i] == '\n' {
			h.breaks = append(h.breaks, i)
		}
	}
	for _, loc := range hostIndicatorPattern.FindAllStringIndex(s, -1) {
		h.lines = append(h.lines, h.lineOf(loc[0]))
	}
	return h
}

// lineOf returns the number of the line that holds the byte offset at.
func (h *hostLines) lineOf(at int) int {
	line, _ := slices.BinarySearch(h.breaks, at)
	return line
}

// near reports whether a host is named within hostLoginWindowLines
// lines of the byte offset at.
func (h *hostLines) near(at int) bool {
	line := h.lineOf(at)
	first, _ := slices.BinarySearch(h.lines, line-hostLoginWindowLines)
	return first < len(h.lines) && h.lines[first] <= line+hostLoginWindowLines
}
