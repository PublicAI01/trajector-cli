package redact

import "regexp"

// Provider-specific deterministic secret patterns.
//
// Detection here is purely prefix + length based: it never depends on
// entropy or the surrounding key name, so it catches low-entropy
// credential formats the other secret layers don't reliably flag.
//
// The betterleaks layer's coverage of these differs per prefix (verified
// against the vendored betterleaks v1.5.0 rule source):
//   - sb_secret_: the supabase-project-api-key rule is a *composite* rule
//     (RequiredRules: supabase-project-url) that only fires when a matching
//     "*.supabase.co" URL is present in the same content, on top of an
//     entropy<=4.0 filter. A secret captured on its own therefore passes
//     straight through regardless of entropy.
//   - sbp_: the supabase-management-token rule fires standalone (no
//     RequiredRules), but only matches an exact 40-character lowercase body
//     and is further filtered by entropy<=3.5 and a two-digit minimum. A
//     high-entropy 40-char sbp_ token captured alone IS caught by
//     betterleaks; what this layer adds for sbp_ is coverage of bodies at
//     other lengths, lower entropy, or without two digits.
//
// Supabase (https://supabase.com/docs/guides/getting-started/api-keys):
//   - sb_secret_...      secret API key (replaces the legacy service_role
//     key; bypasses row-level security, server-side
//     only) — SENSITIVE, always redacted.
//   - sbp_...            personal access token used by the Supabase CLI and
//     Management API — SENSITIVE, always redacted.
//   - sb_publishable_... publishable key (replaces the legacy anon key). It
//     is designed to be embedded in client-side code and
//     is protected by row-level security, so it is NOT a
//     secret and is intentionally NOT redacted here.
//     Redacting it would be false-positive noise; this
//     matches betterleaks, which also ships no
//     publishable-key rule.
//
// The current real key bodies are 31 chars (sb_secret_) and 40 chars
// (sbp_); charsets mirror betterleaks (sb_secret_ keys are mixed-case
// base64url, sbp_ tokens are lowercase). The {20,} floor comfortably
// catches the current and plausibly-longer future formats while rejecting
// short identifier-like collisions such as "sb_secret_short".
//
// Known false-positive class: because the body charset includes `_` and
// the {20,} length check is open-ended, sufficiently long snake_case
// identifiers that merely start with a provider prefix are redacted even
// though they aren't secrets — e.g. `sb_secret_key_rotation_handler`, or
// mid-word inside a longer identifier like
// `libsbp_something_long_enough_value`. This is
// accepted: over-redaction is the safe direction here (see
// TestString_SupabaseProviderTokenLongIdentifierOverRedaction), and adding
// anchors or capping the body length to eliminate it would reopen the
// low-entropy under-redaction gap below.
//
// The prefix is deliberately NOT preceded by a \b word boundary. \b requires
// the character before the prefix to be a non-word char, so a secret glued to
// a preceding word character — an underscore-joined name (FOO_sb_secret_…) or,
// in the JSONL fall-back / raw redact.Bytes path, a JSON escape whose trailing
// letter abuts the prefix (…line1\nsb_secret_…, where the byte before "sb" is
// the literal 'n') — would slip past. Because these low-entropy secrets are
// backed up by no other layer, missing them means the raw key reaches the
// checkpoint blob. Dropping the anchor is redaction-completeness-safe: any
// high-entropy incidental match would already be caught by the entropy layer,
// and a mid-word identifier collision (documented above) only ever
// over-redacts, never under-redacts.
//
// AWS access key ids (https://docs.aws.amazon.com/IAM/latest/UserGuide/reference-identifiers.html):
// the four assignable prefixes (AKIA long-term user key, ASIA temporary
// session key, ABIA bearer token, ACCA context-specific credential) followed
// by a 16-character base32 body. betterleaks' aws-access-token rule matches
// that shape on its own, but it is a *composite* rule (RequiredRules:
// aws-secret-access-key, WithinLines 5): it only fires when a secret access
// key sits within five lines of the id. An id captured alone — in prose, in
// tool output, in the `aws_access_key_id = ...` line of a credentials file,
// or as the value of `AWS_ACCESS_KEY_ID=` — never reaches its filter. The
// body is low-entropy by construction, so the entropy layer misses it too.
// The older A3T prefix is left out: AWS no longer assigns it, and betterleaks
// itself marks it as doubtful.
//
// The documented placeholder AKIAIOSFODNN7EXAMPLE is deliberately NOT
// exempted, although betterleaks skips anything ending in EXAMPLE. Masking a
// placeholder costs a few characters of an example; leaving a real key that
// merely looks like one costs the whole record downstream.
//
// Slack tokens (https://api.slack.com/authentication/token-types): the
// xox<letter>- family. The letters this layer accepts are the ones
// betterleaks' rule catalog knows, each of which pins one body shape: b (bot
// and legacy bot), p and e (user, configuration access and configuration
// refresh), a and r (legacy workspace), o and s (legacy). Their bodies are
// dash-separated runs of alphanumerics and low-entropy, so a token whose body
// drifts from the pinned shape reaches no other layer; the prefix alone says
// the value is a credential, and this layer matches on it. Left out on
// purpose: app-level tokens (xapp-, a different prefix) and session tokens
// (xoxc-, xoxd-), whose bodies carry `/`, `+` and `_` and would be truncated
// by the trailing anchor below; betterleaks covers both standalone.
//
// Neither pattern has a leading \b, for the reason given above for the
// Supabase prefixes: an id glued to a preceding word character — an
// underscore-joined name, or the trailing letter of a JSON escape in the
// raw-line fall-back path — would otherwise slip past, with no other layer
// behind it. The trailing \b stays: an AWS id body is a fixed length and a
// Slack body excludes `_`, so the anchor cannot truncate a real secret, and
// it keeps the fixed-length AWS pattern from firing inside a longer
// uppercase identifier.
var providerTokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sb_secret_[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`sbp_[a-z0-9_-]{20,}`),
	regexp.MustCompile(`(?:AKIA|ASIA|ABIA|ACCA)[A-Z2-7]{16}\b`),
	regexp.MustCompile(`xox[abeoprs]-[0-9A-Za-z-]{10,}\b`),
}

// detectProviderTokens returns tagged regions for every occurrence of a
// known provider secret-token prefix in s. Regions use the empty label so
// they render as the bare "REDACTED" token, consistent with the other
// always-on secret layers.
func detectProviderTokens(s string) []taggedRegion {
	var regions []taggedRegion
	for _, pat := range providerTokenPatterns {
		for _, loc := range pat.FindAllStringIndex(s, -1) {
			regions = append(regions, taggedRegion{region: region{loc[0], loc[1]}})
		}
	}
	return regions
}
