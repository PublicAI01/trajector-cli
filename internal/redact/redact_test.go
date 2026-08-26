package redact_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/redact"
)

// highEntropySecret is a string with Shannon entropy > 4.5 that will trigger redaction.
const highEntropySecret = "sk-ant-api03-xK9mZ2vL8nQ5rT1wY4bC7dF0gH3jE6pA"

var fakeOpenSSHPrivateKey = makeFakeOpenSSHPrivateKey(`b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACB7ZlJ8tkWCKdRJRGF1BngP3bkNbz8bMF6Yl5xLJp9m1QAAAJj2M3UO9jN1
DgAAAAtzc2gtZWQyNTUxOQAAACB7ZlJ8tkWCKdRJRGF1BngP3bkNbz8bMF6Yl5xLJp9m1QA
AAEAGZmFrZS1rZXktZm9yLXJlZGFjdGlvbi10ZXN0LW9ubHkBAgMEBQY=`)

func makeFakeOpenSSHPrivateKey(payload string) string {
	return strings.Join([]string{
		openSSHPrivateKeyMarker("BEGIN"),
		payload,
		openSSHPrivateKeyMarker("END"),
	}, "\n")
}

func openSSHPrivateKeyMarker(kind string) string {
	return "-----" + kind + " " + "OPEN" + "SSH" + " " + "PRIVATE" + " KEY-----"
}

// redactedString feeds raw text through JSONLBytes. Text that is not
// valid JSON takes the raw-line path, so this exercises the full
// layered scan the way a malformed transcript line would.
func redactedString(t testing.TB, s string) string {
	t.Helper()
	rb, err := redact.JSONLBytes([]byte(s))
	if err != nil {
		t.Fatalf("JSONLBytes: %v", err)
	}
	return string(rb.Bytes())
}

// redactedField wraps s as the "content" field of a JSON document, runs
// JSONLBytes, and returns the decoded value of "content" afterwards.
// This drives the production field-aware path end to end.
func redactedField(t testing.TB, s string) string {
	t.Helper()
	doc, err := json.Marshal(map[string]string{"content": s})
	if err != nil {
		t.Fatal(err)
	}
	rb, err := redact.JSONLBytes(doc)
	if err != nil {
		t.Fatalf("JSONLBytes: %v", err)
	}
	var out map[string]string
	if err := json.Unmarshal(rb.Bytes(), &out); err != nil {
		t.Fatalf("redacted output is not valid JSON: %v", err)
	}
	return out["content"]
}

type stringRedactionCase struct {
	name  string
	input string
	want  string
}

// assertFieldRedactionCases runs each case's input through the field-aware
// JSONL path via redactedField.
func assertFieldRedactionCases(t *testing.T, tests []stringRedactionCase) {
	t.Helper()
	assertRedactionCases(t, redactedField, tests)
}

// assertRawRedactionCases runs each case's input through the raw-line
// fall-back path via redactedString.
func assertRawRedactionCases(t *testing.T, tests []stringRedactionCase) {
	t.Helper()
	assertRedactionCases(t, redactedString, tests)
}

func assertRedactionCases(t *testing.T, fn func(testing.TB, string) string, tests []stringRedactionCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := fn(t, tt.input)
			if got != tt.want {
				t.Errorf("redacted %q = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestJSONLBytes_RawTextNoSecrets(t *testing.T) {
	input := []byte("hello world, this is normal text")
	result, err := redact.JSONLBytes(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result.Bytes()) != string(input) {
		t.Errorf("expected unchanged input, got %q", result.Bytes())
	}
	// Should return the original slice when no changes
	if &result.Bytes()[0] != &input[0] {
		t.Error("expected same underlying slice when no redaction needed")
	}
}

func TestJSONLBytes_RawTextWithSecret(t *testing.T) {
	input := []byte("my key is " + highEntropySecret + " ok")
	result, err := redact.JSONLBytes(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []byte("my key is REDACTED ok")
	if !bytes.Equal(result.Bytes(), expected) {
		t.Errorf("got %q, want %q", result.Bytes(), expected)
	}
}

func TestJSONLBytes_NoSecrets(t *testing.T) {
	input := []byte(`{"type":"text","content":"hello"}`)
	result, err := redact.JSONLBytes(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result.Bytes()) != string(input) {
		t.Errorf("expected unchanged input, got %q", result.Bytes())
	}
	if &result.Bytes()[0] != &input[0] {
		t.Error("expected same underlying slice when no redaction needed")
	}
}

func TestJSONLBytes_WithSecret(t *testing.T) {
	input := []byte(`{"type":"text","content":"key=` + highEntropySecret + `"}`)
	result, err := redact.JSONLBytes(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []byte(`{"type":"text","content":"REDACTED"}`)
	if !bytes.Equal(result.Bytes(), expected) {
		t.Errorf("got %q, want %q", result.Bytes(), expected)
	}
}

func TestRedactedBytes_Bytes(t *testing.T) {
	t.Parallel()
	input := []byte(`{"type":"text","content":"hello"}`)
	rb := redact.AlreadyRedacted(input)
	if !bytes.Equal(rb.Bytes(), input) {
		t.Errorf("Bytes() = %q, want %q", rb.Bytes(), input)
	}
}

func TestRedactedBytes_Len(t *testing.T) {
	t.Parallel()
	input := []byte(`some data`)
	rb := redact.AlreadyRedacted(input)
	if rb.Len() != len(input) {
		t.Errorf("Len() = %d, want %d", rb.Len(), len(input))
	}
}

func TestAlreadyRedacted(t *testing.T) {
	t.Parallel()
	input := []byte(`some data`)
	rb := redact.AlreadyRedacted(input)
	if !bytes.Equal(rb.Bytes(), input) {
		t.Errorf("AlreadyRedacted() = %q, want %q", rb.Bytes(), input)
	}
}

func TestJSONLBytes_TopLevelArray(t *testing.T) {
	// Top-level JSON arrays are valid JSONL and should be redacted.
	input := `["` + highEntropySecret + `","normal text"]`
	result := redactedString(t, input)
	expected := `["REDACTED","normal text"]`
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestJSONLBytes_TopLevelArrayNoSecrets(t *testing.T) {
	input := `["hello","world"]`
	result := redactedString(t, input)
	if result != input {
		t.Errorf("expected unchanged input, got %q", result)
	}
}

func TestJSONLBytes_MultipleObjects_AllRedacted(t *testing.T) {
	t.Parallel()
	// Regression test: JSONL with multiple top-level JSON objects must redact
	// secrets in ALL objects, not just the first. The single-JSON fast path must
	// not accidentally consume only the first object and return early.
	input := `{"content":"safe text","id":"abc"}
{"content":"key=` + highEntropySecret + `","id":"def"}
{"content":"also safe","id":"ghi"}`

	result := redactedString(t, input)

	// The secret in the second line should be redacted.
	if strings.Contains(result, highEntropySecret) {
		t.Error("secret in second JSONL object was not redacted")
	}
	if !strings.Contains(result, "REDACTED") {
		t.Error("expected REDACTED in output")
	}

	// IDs should be preserved (field-aware skip).
	for _, id := range []string{"abc", "def", "ghi"} {
		if !strings.Contains(result, id) {
			t.Errorf("ID %q should be preserved", id)
		}
	}

	// Non-secret content should be preserved.
	if !strings.Contains(result, "safe text") {
		t.Error("safe text in first object was corrupted")
	}
	if !strings.Contains(result, "also safe") {
		t.Error("safe text in third object was corrupted")
	}
}

func TestJSONLBytes_InvalidJSONLine(t *testing.T) {
	// Lines that aren't valid JSON should be processed with normal string redaction.
	input := `{"type":"text", "invalid ` + highEntropySecret + " json"
	result := redactedString(t, input)
	expected := `{"type":"text", "invalid REDACTED json`
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestJSONLBytes_FieldSkipPolicy(t *testing.T) {
	t.Parallel()
	// For each key, a document {"<key>":"<high-entropy secret>"} is run through
	// JSONLBytes. Protected keys (signatures, IDs, paths) must keep the value
	// verbatim; every other key must have it masked.
	tests := []struct {
		key     string
		skipped bool
	}{
		// Fields ending in "id" should be skipped.
		{"id", true},
		{"session_id", true},
		{"sessionId", true},
		{"trace_id", true},
		{"traceID", true},
		{"userId", true},
		// Fields ending in "ids" should be skipped.
		{"ids", true},
		{"session_ids", true},
		{"userIds", true},
		// Signature fields should be skipped (any key ending in "signature").
		{"signature", true},
		{"thinkingSignature", true},
		{"thinking_signature", true},
		// Path-related fields should be skipped.
		{"filePath", true},
		{"file_path", true},
		{"cwd", true},
		{"root", true},
		{"directory", true},
		{"dir", true},
		{"path", true},
		// Fields that should NOT be skipped.
		{"content", false},
		{"type", false},
		{"name", false},
		{"text", false},
		{"output", false},
		{"input", false},
		{"command", false},
		{"args", false},
		{"video", false},      // ends in "o", not "id"
		{"identify", false},   // ends in "ify", not "id"
		{"signatures", false}, // does not end in "signature"
		{"signal_data", false},
		{"consideration", false}, // contains "id" but doesn't end with it
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()
			doc, err := json.Marshal(map[string]string{tt.key: highEntropySecret})
			if err != nil {
				t.Fatal(err)
			}
			rb, err := redact.JSONLBytes(doc)
			if err != nil {
				t.Fatalf("JSONLBytes: %v", err)
			}
			var out map[string]string
			if err := json.Unmarshal(rb.Bytes(), &out); err != nil {
				t.Fatalf("redacted output is not valid JSON: %v", err)
			}
			want := "REDACTED"
			if tt.skipped {
				want = highEntropySecret
			}
			if out[tt.key] != want {
				t.Errorf("value of %q = %q, want %q", tt.key, out[tt.key], want)
			}
		})
	}
}

func TestJSONLBytes_SkippedFieldValueCollision(t *testing.T) {
	t.Parallel()
	input := `{"session_id":"` + highEntropySecret + `","content":"` + highEntropySecret + `"}`

	result := redactedString(t, input)

	if !strings.Contains(result, `"session_id":"`+highEntropySecret+`"`) {
		t.Fatalf("expected skipped session_id to be preserved, got: %s", result)
	}
	if !strings.Contains(result, `"content":"REDACTED"`) {
		t.Fatalf("expected content field to be redacted, got: %s", result)
	}
}

func TestJSONLBytes_ArrayElementCollisionPreservesProtectedFields(t *testing.T) {
	t.Parallel()
	// An array element is collected without an owning key, so its replacement
	// applies to any string token with the same decoded value. When that value
	// collides with the value of a protected field (path, id, signature), the
	// protected field must stay byte-for-byte intact: observed values are never
	// rewritten. Only the array copy is masked.
	input := `{"file_path":"` + highEntropySecret + `","cwd":"` + highEntropySecret + `","thinkingSignature":"` + highEntropySecret + `","session_id":"` + highEntropySecret + `","attachments":["` + highEntropySecret + `"]}`

	got := redactedString(t, input)
	for _, field := range []string{"file_path", "cwd", "thinkingSignature", "session_id"} {
		if !strings.Contains(got, `"`+field+`":"`+highEntropySecret+`"`) {
			t.Errorf("protected field %q was rewritten: %s", field, got)
		}
	}
	if !strings.Contains(got, `"attachments":["REDACTED"]`) {
		t.Errorf("array copy of the secret was not masked: %s", got)
	}
}

func TestJSONLBytes_ArrayUnderSkippedKeyPreserved(t *testing.T) {
	t.Parallel()
	// A container that is the value of a protected field is protected as a
	// whole: elements of an ids array keep their observed values even though
	// the same value is masked in an unprotected position.
	input := `{"session_ids":["` + highEntropySecret + `"],"content":"` + highEntropySecret + `"}`

	got := redactedString(t, input)
	if !strings.Contains(got, `"session_ids":["`+highEntropySecret+`"]`) {
		t.Errorf("element of protected ids array was rewritten: %s", got)
	}
	if !strings.Contains(got, `"content":"REDACTED"`) {
		t.Errorf("content field was not masked: %s", got)
	}
}

func TestJSONLBytes_ObjectKeyNeverReplaced(t *testing.T) {
	t.Parallel()
	// An object key spelled exactly like a replaced value must never be
	// rewritten: keys are structure, and masking one changes the document's
	// shape instead of hiding a secret.
	input := `{"items":["` + highEntropySecret + `"],"` + highEntropySecret + `":"safe"}`

	got := redactedString(t, input)
	if !strings.Contains(got, `"`+highEntropySecret+`":"safe"`) {
		t.Errorf("object key was rewritten: %s", got)
	}
	if !strings.Contains(got, `"items":["REDACTED"]`) {
		t.Errorf("array copy of the secret was not masked: %s", got)
	}
}

func TestJSONLBytes_PreservesThinkingSignature(t *testing.T) {
	t.Parallel()
	// Extended-thinking signatures are stored under keys like
	// "thinkingSignature". Their base64 value is high-entropy; redacting it
	// corrupts the signature, which must be preserved verbatim.
	input := `{"type":"thinking","thinking":"plan","thinkingSignature":"` + highEntropySecret + `"}`

	result := redactedString(t, input)
	if !strings.Contains(result, `"thinkingSignature":"`+highEntropySecret+`"`) {
		t.Fatalf("expected thinkingSignature to be preserved verbatim, got: %s", result)
	}
}

func TestPatternDetection(t *testing.T) {
	// These secrets have entropy ~3.9, below the 4.5 threshold, so
	// entropy-only detection misses them. Betterleaks pattern matching
	// should catch them.
	assertFieldRedactionCases(t, []stringRedactionCase{
		{
			name:  "AWS access key (entropy ~3.9, below 4.5 threshold)",
			input: "key=AKIAYRWQG5EJLPZLBYNP",
			want:  "key=REDACTED",
		},
		{
			name:  "two AWS keys separated by space produce two REDACTED tokens",
			input: "key=AKIAYRWQG5EJLPZLBYNP AKIAYRWQG5EJLPZLBYNP",
			want:  "key=REDACTED REDACTED",
		},
		{
			name:  "adjacent AWS keys without separator merge into single REDACTED",
			input: "key=AKIAYRWQG5EJLPZLBYNPAKIAYRWQG5EJLPZLBYNP",
			want:  "key=REDACTED",
		},
	})
}

// supabaseSecretPrefix, supabasePersonalPrefix, and supabasePublishablePrefix
// assemble the Supabase credential prefixes from fragments so a complete token
// never appears verbatim in source. This mirrors openSSHPrivateKeyMarker above
// and keeps secret scanners (including GitHub push protection) from flagging
// synthetic test fixtures; the assembled runtime values exercise the redactor
// exactly as a real token would.
func supabaseSecretPrefix() string      { return "sb" + "_secret_" }
func supabasePersonalPrefix() string    { return "sb" + "p_" }
func supabasePublishablePrefix() string { return "sb" + "_publishable_" }

// TestSupabaseProviderTokens covers issue #1716: Supabase sb_secret_
// API keys and sbp_ personal access tokens are low-entropy and, captured in
// isolation, are missed by the entropy layer (threshold 4.5; the issue reports
// entropy 4.199 for the sb_secret_ probe value). betterleaks coverage differs
// per prefix: its sb_secret_ rule is a composite rule that only fires when a
// *.supabase.co URL is co-present, so a bare sb_secret_ value never reaches
// its filter at all; its sbp_ rule fires standalone but requires an exact
// 40-character lowercase body, so bodies of another length (like the probe
// values below) never match its regex regardless of entropy. The deterministic
// provider-prefix layer must catch both regardless of entropy, body length, or
// the surrounding variable name.
func TestSupabaseProviderTokens(t *testing.T) {
	t.Parallel()

	secret := supabaseSecretPrefix() + "probe_20260710_7f91c2d8e4a6b3f0" // entropy 4.199, below the 4.5 threshold
	realSecret := supabaseSecretPrefix() + "9uM4GhB0STF5R4K3HxQtlg_bzWW6DRj"
	sbpToken := supabasePersonalPrefix() + "test_probe_20260710_test_probe_2026071" // also below the entropy threshold
	// Real Supabase key bodies are base64url, which includes '-'. No other
	// fixture in this test contains a hyphen, so the charset's '-' member is
	// otherwise unpinned: narrowing [A-Za-z0-9_-] / [a-z0-9_-] to drop the
	// hyphen would still pass every other case here while silently truncating
	// (not merely shrinking) the match at the first hyphen in a real key,
	// leaking the remainder raw — the #1716 failure mode recurring via an
	// innocent charset "tidy-up".
	secretWithHyphen := supabaseSecretPrefix() + "probe-20260710-7f91c2d8e4a6b3f0"
	sbpTokenWithHyphen := supabasePersonalPrefix() + "probe-20260710-7f91c2d8e4a6b3f0"

	assertFieldRedactionCases(t, []stringRedactionCase{
		{
			name:  "sb_secret_ standalone (issue #1716 repro value)",
			input: secret,
			want:  "REDACTED",
		},
		{
			name:  "sb_secret_ at start of line",
			input: secret + " is the service_role key",
			want:  "REDACTED is the service_role key",
		},
		{
			name:  "sb_secret_ at end of line",
			input: "service_role key: " + secret,
			want:  "service_role key: REDACTED",
		},
		{
			// Canonical .env form. The chosen token value is low-entropy
			// (quoting has no effect on entropy-layer matching), so the
			// entropy layer misses it, isolating the deterministic provider
			// layer.
			name:  "sb_secret_ in env-style double-quoted assignment",
			input: `SUPABASE_SERVICE_ROLE_KEY="` + secret + `"`,
			want:  `SUPABASE_SERVICE_ROLE_KEY="REDACTED"`,
		},
		{
			name:  "sb_secret_ single-quoted value",
			input: "key: '" + secret + "'",
			want:  "key: 'REDACTED'",
		},
		{
			name:  "sb_secret_ multiple occurrences",
			input: secret + " then " + secret,
			want:  "REDACTED then REDACTED",
		},
		{
			name:  "sb_secret_ real-shaped mixed-case body",
			input: `SUPABASE_SERVICE_ROLE_KEY="` + realSecret + `"`,
			want:  `SUPABASE_SERVICE_ROLE_KEY="REDACTED"`,
		},
		{
			name:  "sbp_ personal access token (38-char body, betterleaks' rule requires exactly 40)",
			input: "SUPABASE_ACCESS_TOKEN=" + sbpToken,
			want:  "SUPABASE_ACCESS_TOKEN=REDACTED",
		},
		{
			name:  "sb_secret_ body with an early hyphen (real base64url shape)",
			input: secretWithHyphen,
			want:  "REDACTED",
		},
		{
			name:  "sbp_ body with an early hyphen (real base64url shape)",
			input: sbpTokenWithHyphen,
			want:  "REDACTED",
		},
	})
}

// TestSupabaseProviderTokenLengthBoundaries pins the {20,} body-length
// floor shared by both provider patterns as an explicit boundary rather than
// an emergent property of an unrelated fixture: a body of exactly 20 chars
// must redact, and a body of exactly 19 chars must be preserved. Before this
// test, the floor was pinned only accidentally — via key_rotation_handler
// (sb_secret_) happening to have a 20-char body, with no equivalent coverage
// for sbp_ at all. Each case fails if either pattern's minimum is tightened
// to {21,}.
func TestSupabaseProviderTokenLengthBoundaries(t *testing.T) {
	t.Parallel()

	const (
		body20 = "boundary_probe_2026x" // exactly 20 chars
		body19 = "boundary_probe_2026"  // exactly 19 chars
	)
	if len(body20) != 20 || len(body19) != 19 {
		t.Fatalf("fixture bodies are %d/%d chars, want 20/19", len(body20), len(body19))
	}

	secret20 := supabaseSecretPrefix() + body20
	secret19 := supabaseSecretPrefix() + body19
	sbp20 := supabasePersonalPrefix() + body20
	sbp19 := supabasePersonalPrefix() + body19

	assertFieldRedactionCases(t, []stringRedactionCase{
		{
			name:  "sb_secret_ with exactly 20-char body redacts",
			input: secret20,
			want:  "REDACTED",
		},
		{
			name:  "sb_secret_ with exactly 19-char body is preserved",
			input: secret19,
			want:  secret19,
		},
		{
			name:  "sbp_ with exactly 20-char body redacts",
			input: sbp20,
			want:  "REDACTED",
		},
		{
			name:  "sbp_ with exactly 19-char body is preserved",
			input: sbp19,
			want:  sbp19,
		},
	})
}

// TestSupabaseProviderTokenOverRedactionGuards pins that the
// deterministic provider layer does not over-redact. Publishable keys are
// designed to be embedded in client code and are intentionally not targeted by
// this layer (a low-entropy publishable value therefore passes through it; a
// high-entropy real one would still be caught by the entropy layer). A bare
// prefix or a prefix with a too-short body is not a credential.
func TestSupabaseProviderTokenOverRedactionGuards(t *testing.T) {
	t.Parallel()

	publishable := supabasePublishablePrefix() + "probe_20260710_7f91c2d8e4a6b3f0"
	shortSecret := supabaseSecretPrefix() + "short"
	shortToken := supabasePersonalPrefix() + "short"

	assertFieldRedactionCases(t, []stringRedactionCase{
		{
			// The publishable fixture is low-entropy (quoting has no effect on
			// entropy-layer matching), so the entropy layer does not flag it,
			// proving the provider layer itself does not target publishable
			// keys.
			name:  "publishable key is not targeted by the provider layer",
			input: `NEXT_PUBLIC_SUPABASE_KEY="` + publishable + `"`,
			want:  `NEXT_PUBLIC_SUPABASE_KEY="` + publishable + `"`,
		},
		{
			name:  "sb_secret_ with too-short body is preserved",
			input: shortSecret,
			want:  shortSecret,
		},
		{
			name:  "sbp_ with too-short body is preserved",
			input: shortToken,
			want:  shortToken,
		},
		{
			name:  "bare sb_secret_ prefix in prose is preserved",
			input: "the " + supabaseSecretPrefix() + " prefix identifies Supabase secret keys",
			want:  "the " + supabaseSecretPrefix() + " prefix identifies Supabase secret keys",
		},
	})
}

// TestSupabaseProviderTokenLongIdentifierOverRedaction documents a
// known, accepted false-positive class: because the body charset includes
// underscore and the length check is {20,} with no upper bound, sufficiently
// long snake_case identifiers that merely start with a provider prefix are
// redacted even though they are not secrets — including mid-word, since the
// prefix is deliberately not anchored (see the package comment in
// providers.go). This is intentional: over-redaction is the safe direction,
// and reintroducing a \b anchor or a body-length cap to "fix" this would
// reopen the low-entropy under-redaction gap the provider layer exists to
// close. This test pins the tradeoff so it isn't silently reversed.
func TestSupabaseProviderTokenLongIdentifierOverRedaction(t *testing.T) {
	t.Parallel()

	assertFieldRedactionCases(t, []stringRedactionCase{
		{
			name:  "long snake_case identifier starting with sb_secret_ is over-redacted",
			input: "func " + supabaseSecretPrefix() + "key_rotation_handler() {}",
			want:  "func REDACTED() {}",
		},
		{
			name:  "sbp_ mid-word inside a longer identifier is over-redacted",
			input: "call lib" + supabasePersonalPrefix() + "something_long_enough_value()",
			want:  "call libREDACTED()",
		},
	})
}

// TestJSONLBytes_SupabaseSecretRedacted drives the secret through the
// field-aware JSONL path, mirroring a Claude Code transcript line where
// the secret lives in a message-content leaf.
func TestJSONLBytes_SupabaseSecretRedacted(t *testing.T) {
	t.Parallel()
	secret := supabaseSecretPrefix() + "probe_20260710_7f91c2d8e4a6b3f0"
	line := `{"type":"user","message":{"role":"user","content":"the service_role key is ` + secret + ` now"}}`
	got := redactedString(t, line)
	if strings.Contains(got, secret) {
		t.Fatalf("secret survived JSONL redaction: %q", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Fatalf("expected REDACTED placeholder in %q", got)
	}
}

// TestSupabaseProviderTokenBoundaries pins that the provider layer
// redacts a Supabase secret even when the prefix abuts a preceding *word*
// character. A \b anchor before the prefix only fires after a non-word
// character, so a secret glued to a preceding letter/digit/underscore — an
// underscore-joined name, or (in the JSONL raw-line fall-back path that scans
// undecoded text) a JSON escape whose trailing letter sits against the prefix,
// e.g. "…line1\nsb_secret_…" where the byte before "sb" is the literal 'n' —
// would slip past. These bodies are deliberately low-entropy, so no other
// layer backs the provider layer up: a miss reaches the blob raw. Each case
// fails if the leading \b anchor is reintroduced.
func TestSupabaseProviderTokenBoundaries(t *testing.T) {
	t.Parallel()

	secret := supabaseSecretPrefix() + "probe_20260710_7f91c2d8e4a6b3f0"
	sbpToken := supabasePersonalPrefix() + "test_probe_20260710_test_probe_2026071"

	assertRawRedactionCases(t, []stringRedactionCase{
		{
			name:  "sb_secret_ glued to a preceding word char",
			input: "x" + secret,
			want:  "xREDACTED",
		},
		{
			// Raw-text fall-back shape: the transcript line failed to parse as
			// JSON, so the scan runs on the undecoded bytes where "\n" is a
			// literal backslash-n and the 'n' abuts the prefix.
			name:  "sb_secret_ preceded by a literal JSON escape letter",
			input: `first line\n` + secret,
			want:  `first line\nREDACTED`,
		},
		{
			name:  "sbp_ preceded by a literal JSON escape letter",
			input: `first line\n` + sbpToken,
			want:  `first line\nREDACTED`,
		},
	})
}

// TestJSONLBytes_SupabaseSecretMalformedLineFallback drives the secret
// through the JSONL fall-back branch (the raw-line scan runs on lines where
// json.Unmarshal fails), with the secret glued to a literal "\n" escape so
// the byte before the prefix is a word char. This is the realistic path by
// which a malformed/truncated transcript line could leak a low-entropy
// Supabase secret; it must still be redacted.
func TestJSONLBytes_SupabaseSecretMalformedLineFallback(t *testing.T) {
	t.Parallel()
	secret := supabaseSecretPrefix() + "probe_20260710_7f91c2d8e4a6b3f0"
	// Trailing garbage after the closing brace makes json.Unmarshal fail, forcing
	// the raw-line fall-back; inside, "\n" is a literal backslash-n before "sb".
	line := `{"content":"line1\n` + secret + `"} <-- truncated`
	got := redactedString(t, line)
	if strings.Contains(got, secret) {
		t.Fatalf("secret survived JSONL fall-back redaction: %q", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Fatalf("expected REDACTED placeholder in %q", got)
	}
}

func TestCredentialedURIs(t *testing.T) {
	assertFieldRedactionCases(t, []stringRedactionCase{
		{
			name:  "postgres URI",
			input: "DATABASE_URL=postgres://app:pwd123@db.example.com:5432/app",
			want:  "DATABASE_URL=REDACTED",
		},
		{
			name:  "postgresql URI with query",
			input: `dsn="postgresql://svc:moderatepw@localhost/app?sslmode=require"`,
			want:  `dsn="REDACTED"`,
		},
		{
			name:  "mongodb srv URI",
			input: "mongo=mongodb+srv://user:pass123@cluster0.example.mongodb.net/app?retryWrites=true",
			want:  "mongo=REDACTED",
		},
		{
			name:  "mysql URI",
			input: "mysql://root:p@localhost:3306/app",
			want:  "REDACTED",
		},
		{
			name:  "redis URI with empty username",
			input: "cache redis://:hunter2@localhost:6379/0",
			want:  "cache REDACTED",
		},
		{
			name:  "generic credentialed URL",
			input: "proxy=https://user:pass@example.com/path",
			want:  "proxy=REDACTED",
		},
		{
			name:  "URL without password is preserved",
			input: "repo=ssh://git@github.com/PublicAI01/trajector-cli",
			want:  "repo=ssh://git@github.com/PublicAI01/trajector-cli",
		},
		{
			name:  "colon and at-sign in path are preserved",
			input: "url=https://example.com/a:b@c",
			want:  "url=https://example.com/a:b@c",
		},
	})
}

func TestDatabaseConnectionStringRedaction(t *testing.T) {
	t.Parallel()
	assertFieldRedactionCases(t, []stringRedactionCase{
		{
			name:  "postgres keyword DSN",
			input: `dsn="host=db.example.com port=5432 user=svc password=secret dbname=app sslmode=require"`,
			want:  `dsn="REDACTED"`,
		},
		{
			name:  "postgres keyword DSN different order",
			input: "password=secret sslmode=require user=svc host=db.example.com dbname=app",
			want:  "REDACTED",
		},
		{
			name:  "sql server connection string",
			input: "conn=Server=tcp:db.example.com,1433;Database=app;User Id=svc;Password=secret;Encrypt=true",
			want:  "conn=REDACTED",
		},
		{
			name:  "odbc connection string",
			input: "conn=Driver={ODBC Driver 18 for SQL Server};Server=db;UID=svc;PWD=secret;Database=app",
			want:  "conn=REDACTED",
		},
		{
			name:  "jdbc query password",
			input: "jdbc:postgresql://db.example.com:5432/app?user=svc&password=secret&ssl=true",
			want:  "REDACTED",
		},
		{
			name:  "postgres URL query password without userinfo",
			input: "DATABASE_URL=postgresql://db.example.com:5432/app?user=svc&password=secret&sslmode=require",
			want:  "DATABASE_URL=REDACTED",
		},
		{
			name:  "postgres URL query password is case-insensitive",
			input: "DATABASE_URL=postgresql://db.example.com:5432/app?user=svc&Password=secret&sslmode=require",
			want:  "DATABASE_URL=REDACTED",
		},
		{
			name:  "mongodb URL query password without userinfo",
			input: "MONGO_URL=mongodb://cluster0.example.mongodb.net/app?authSource=admin&username=svc&password=secret",
			want:  "MONGO_URL=REDACTED",
		},
		{
			name:  "mongodb srv URL query password without userinfo",
			input: "MONGO_URL=mongodb+srv://cluster0.example.mongodb.net/app?authSource=admin&username=svc&password=secret",
			want:  "MONGO_URL=REDACTED",
		},
		{
			name:  "placeholder password in database URL query is preserved",
			input: "DATABASE_URL=postgresql://db.example.com/app?user=svc&password=${DB_PASSWORD}",
			want:  "DATABASE_URL=postgresql://db.example.com/app?user=svc&password=${DB_PASSWORD}",
		},
		{
			name:  "jdbc semicolon password",
			input: "jdbc:sqlserver://db.example.com:1433;databaseName=app;user=svc;password=secret;encrypt=true",
			want:  "REDACTED",
		},
		{
			name:  "ado.net quoted password with embedded semicolons",
			input: `conn=Server=db.example.com;User ID=svc;Password="se;cret;here";Encrypt=true`,
			want:  "conn=REDACTED",
		},
		{
			name:  "ado.net single-quoted password with embedded semicolons",
			input: `conn=Server=db.example.com;User ID=svc;Password='se;cret;here';Encrypt=true`,
			want:  "conn=REDACTED",
		},
	})
}

func TestBoundedCredentialValueRedaction(t *testing.T) {
	t.Parallel()
	assertFieldRedactionCases(t, []stringRedactionCase{
		{
			name:  "db password env var",
			input: "DB_PASSWORD=secret123",
			want:  "DB_PASSWORD=REDACTED",
		},
		{
			name:  "postgres password env var",
			input: "PGPASSWORD='secret123'",
			want:  "PGPASSWORD='REDACTED'",
		},
		{
			name:  "redis password env var",
			input: `REDIS_PASSWORD="secret123"`,
			want:  `REDIS_PASSWORD="REDACTED"`,
		},
		{
			name:  "lowercase database password",
			input: "database_password=secret123",
			want:  "database_password=REDACTED",
		},
		{
			name:  "prefixed db password env var",
			input: "APP_DB_PASSWORD=secret123",
			want:  "APP_DB_PASSWORD=REDACTED",
		},
		{
			name:  "prefixed mysql password env var",
			input: "PROD_MYSQL_PWD=secret123",
			want:  "PROD_MYSQL_PWD=REDACTED",
		},
		{
			name:  "mysql root password env var",
			input: "MYSQL_ROOT_PASSWORD=secret123",
			want:  "MYSQL_ROOT_PASSWORD=REDACTED",
		},
		{
			name:  "mariadb root password env var",
			input: "MARIADB_ROOT_PASSWORD=secret123",
			want:  "MARIADB_ROOT_PASSWORD=REDACTED",
		},
		{
			name:  "mongo initdb root password env var",
			input: "MONGO_INITDB_ROOT_PASSWORD=secret123",
			want:  "MONGO_INITDB_ROOT_PASSWORD=REDACTED",
		},
		{
			name:  "mssql sa password env var",
			input: "MSSQL_SA_PASSWORD=secret123",
			want:  "MSSQL_SA_PASSWORD=REDACTED",
		},
		{
			name:  "double underscore separator",
			input: "DB__PASSWORD=secret123",
			want:  "DB__PASSWORD=REDACTED",
		},
	})
}

func TestBoundedCredentialValueOverRedactionGuards(t *testing.T) {
	t.Parallel()
	assertFieldRedactionCases(t, []stringRedactionCase{
		{
			name:  "placeholder env var is preserved",
			input: "DB_PASSWORD=${DB_PASSWORD}",
			want:  "DB_PASSWORD=${DB_PASSWORD}",
		},
		{
			name:  "already redacted value is preserved",
			input: "DB_PASSWORD=REDACTED",
			want:  "DB_PASSWORD=REDACTED",
		},
		{
			name:  "prose about password is preserved",
			input: "the password field should be rotated regularly",
			want:  "the password field should be rotated regularly",
		},
		{
			name:  "generic key is preserved",
			input: "key=not-a-secret-setting",
			want:  "key=not-a-secret-setting",
		},
		{
			name:  "shell pwd is preserved",
			input: "PWD=/workspace/project",
			want:  "PWD=/workspace/project",
		},
		{
			name:  "standalone password assignment is preserved",
			input: "password=not-a-secret-setting",
			want:  "password=not-a-secret-setting",
		},
		{
			name:  "password reset query parameter is preserved",
			input: "https://example.com/?password_reset=true",
			want:  "https://example.com/?password_reset=true",
		},
		{
			name:  "generic https password query is preserved",
			input: "https://example.com/callback?user=svc&password=not-a-db-credential&debug=true",
			want:  "https://example.com/callback?user=svc&password=not-a-db-credential&debug=true",
		},
		{
			name:  "db password hash field is preserved",
			input: "DB_PASSWORD_HASH=abcdef",
			want:  "DB_PASSWORD_HASH=abcdef",
		},
		{
			name:  "non-credential mysql field is preserved",
			input: "MYSQL_USER_ID=alice",
			want:  "MYSQL_USER_ID=alice",
		},
		{
			name:  "angle bracket placeholder is preserved",
			input: "DB_PASSWORD=<password>",
			want:  "DB_PASSWORD=<password>",
		},
		{
			name:  "your_password placeholder is preserved",
			input: "DB_PASSWORD=your_password",
			want:  "DB_PASSWORD=your_password",
		},
		{
			name:  "your-db-password placeholder is preserved",
			input: "DB_PASSWORD=<your-db-password>",
			want:  "DB_PASSWORD=<your-db-password>",
		},
		{
			name:  "asterisk mask placeholder is preserved",
			input: "DB_PASSWORD=*****",
			want:  "DB_PASSWORD=*****",
		},
		{
			name:  "dot mask placeholder is preserved",
			input: "DB_PASSWORD=......",
			want:  "DB_PASSWORD=......",
		},
		{
			name:  "secret_here placeholder is preserved",
			input: "DB_PASSWORD=secret_here",
			want:  "DB_PASSWORD=secret_here",
		},
		{
			name:  "placeholder literal is preserved",
			input: "DB_PASSWORD=placeholder",
			want:  "DB_PASSWORD=placeholder",
		},
	})
}

// Pins that single-char "masks" and arbitrary <…> wrappers do NOT count as
// placeholders, so credentials that happen to be short or bracket-wrapped
// still get redacted. The opposite cases (`***`, `<password>`, etc.) are
// covered above in TestBoundedCredentialValueOverRedactionGuards.
func TestShortAndOpaquePlaceholdersFallThrough(t *testing.T) {
	t.Parallel()
	assertFieldRedactionCases(t, []stringRedactionCase{
		{
			name:  "single x is not a mask",
			input: "DB_PASSWORD=x",
			want:  "DB_PASSWORD=REDACTED",
		},
		{
			name:  "single dash is not a mask",
			input: "DB_PASSWORD=-",
			want:  "DB_PASSWORD=REDACTED",
		},
		{
			name:  "single asterisk is not a mask",
			input: "DB_PASSWORD=*",
			want:  "DB_PASSWORD=REDACTED",
		},
		{
			name:  "two-char repeat is not a mask",
			input: "DB_PASSWORD=xx",
			want:  "DB_PASSWORD=REDACTED",
		},
		{
			name:  "bracketed value with digits is not a placeholder",
			input: "DB_PASSWORD=<hunter2>",
			want:  "DB_PASSWORD=REDACTED",
		},
		{
			name:  "bracketed mixed-case value is not a placeholder",
			input: "DB_PASSWORD=<RealPassword>",
			want:  "DB_PASSWORD=REDACTED",
		},
	})
}

func TestOpenSSHPrivateKeyBlock(t *testing.T) {
	input := "key:\n" + fakeOpenSSHPrivateKey + "\nend"
	want := "key:\nREDACTED\nend"

	got := redactedField(t, input)
	if got != want {
		t.Errorf("redacted private key block = %q, want %q", got, want)
	}
	if strings.Contains(got, openSSHPrivateKeyMarker("BEGIN")) || strings.Contains(got, openSSHPrivateKeyMarker("END")) {
		t.Errorf("private key block markers should be fully redacted, got %q", got)
	}
}

func TestJSONLBytes_CredentialedURI(t *testing.T) {
	input := `{"type":"text","content":"DATABASE_URL=postgres://app:pwd123@db.example.com:5432/app"}`
	result := redactedString(t, input)

	if strings.Contains(result, "postgres://app:pwd123@db.example.com:5432/app") {
		t.Error("credentialed database URI was not redacted")
	}
	if !strings.Contains(result, "DATABASE_URL=REDACTED") {
		t.Errorf("expected credentialed URI replacement, got %q", result)
	}
}

func TestJSONLBytes_OpenSSHPrivateKeyBlock(t *testing.T) {
	content, err := json.Marshal("key:\n" + fakeOpenSSHPrivateKey + "\nend")
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	input := `{"type":"text","content":` + string(content) + `}`

	result := redactedString(t, input)
	if strings.Contains(result, openSSHPrivateKeyMarker("BEGIN")) || strings.Contains(result, openSSHPrivateKeyMarker("END")) {
		t.Errorf("private key block markers should be fully redacted, got %q", result)
	}
	if !strings.Contains(result, `key:\nREDACTED\nend`) {
		t.Errorf("expected whole private key block replacement, got %q", result)
	}
}

func TestJSONLBytes_DatabaseCredentialRedaction(t *testing.T) {
	t.Parallel()
	input := `{"type":"assistant","message":"dsn host=db.example.com user=svc password=secret dbname=app and env DB_PASSWORD=secret123","session_id":"ses_37273a1fdffegpYbwUTqEkPsQ0","file_path":"/tmp/TestE2E_ExistingFiles/controller.go"}`

	result := redactedString(t, input)
	for _, leaked := range []string{"password=secret", "DB_PASSWORD=secret123"} {
		if strings.Contains(result, leaked) {
			t.Fatalf("expected %q to be redacted, got: %s", leaked, result)
		}
	}
	for _, preserved := range []string{"ses_37273a1fdffegpYbwUTqEkPsQ0", "/tmp/TestE2E_ExistingFiles/controller.go"} {
		if !strings.Contains(result, preserved) {
			t.Fatalf("expected structural value %q to be preserved, got: %s", preserved, result)
		}
	}
}

func TestJSONLBytes_StructuredCredentialFieldsRedacted(t *testing.T) {
	t.Parallel()
	input := `{"type":"assistant","env":{"DB_PASSWORD":"correct-horse-db","REDIS_PASSWORD":"${REDIS_PASSWORD}","note":"correct-horse-db"},"db":{"password":"correct-horse-db","host":"db.example.com","user":"svc"},"session_id":"ses_37273a1fdffegpYbwUTqEkPsQ0"}`

	result := redactedString(t, input)
	for _, leaked := range []string{`"DB_PASSWORD":"correct-horse-db"`, `"password":"correct-horse-db"`} {
		if strings.Contains(result, leaked) {
			t.Fatalf("expected structured credential field %q to be redacted, got: %s", leaked, result)
		}
	}
	for _, preserved := range []string{
		`"DB_PASSWORD":"REDACTED"`,
		`"REDIS_PASSWORD":"${REDIS_PASSWORD}"`,
		`"password":"REDACTED"`,
		`"host":"db.example.com"`,
		`"user":"svc"`,
		`"note":"correct-horse-db"`,
		"ses_37273a1fdffegpYbwUTqEkPsQ0",
	} {
		if !strings.Contains(result, preserved) {
			t.Fatalf("expected %q to be preserved, got: %s", preserved, result)
		}
	}
}

func TestJSONLBytes_NormalizedCredentialKeysRedacted(t *testing.T) {
	t.Parallel()
	input := `{"type":"assistant","env":{"DB Password":"correct-horse-db","note":"correct-horse-db"},"session_id":"ses_37273a1fdffegpYbwUTqEkPsQ0"}`

	result := redactedString(t, input)
	for _, preserved := range []string{
		`"DB Password":"REDACTED"`,
		`"note":"correct-horse-db"`,
		"ses_37273a1fdffegpYbwUTqEkPsQ0",
	} {
		if !strings.Contains(result, preserved) {
			t.Fatalf("expected %q to be preserved, got: %s", preserved, result)
		}
	}
	if strings.Contains(result, `"DB Password":"correct-horse-db"`) {
		t.Fatalf("expected normalized credential key to be redacted, got: %s", result)
	}
}

func TestJSONLBytes_DottedCredentialKeysRedacted(t *testing.T) {
	t.Parallel()
	input := `{"config":{"db.password":"correct-horse-db","mysql.root.password":"correct-horse-mysql","note":"correct-horse-db"}}`

	result := redactedString(t, input)
	for _, redacted := range []string{
		`"db.password":"REDACTED"`,
		`"mysql.root.password":"REDACTED"`,
	} {
		if !strings.Contains(result, redacted) {
			t.Fatalf("expected %q in output, got: %s", redacted, result)
		}
	}
	if !strings.Contains(result, `"note":"correct-horse-db"`) {
		t.Fatalf("expected unrelated note field to be preserved, got: %s", result)
	}
}

func TestJSONLBytes_RootPasswordJSONKeysRedacted(t *testing.T) {
	t.Parallel()
	input := `{"env":{"MYSQL_ROOT_PASSWORD":"correct-horse-mysql","MONGO_INITDB_ROOT_PASSWORD":"correct-horse-mongo","MSSQL_SA_PASSWORD":"correct-horse-mssql"}}`

	result := redactedString(t, input)
	for _, redacted := range []string{
		`"MYSQL_ROOT_PASSWORD":"REDACTED"`,
		`"MONGO_INITDB_ROOT_PASSWORD":"REDACTED"`,
		`"MSSQL_SA_PASSWORD":"REDACTED"`,
	} {
		if !strings.Contains(result, redacted) {
			t.Fatalf("expected %q in output, got: %s", redacted, result)
		}
	}
	for _, leaked := range []string{"correct-horse-mysql", "correct-horse-mongo", "correct-horse-mssql"} {
		if strings.Contains(result, leaked) {
			t.Fatalf("expected %q to be redacted, got: %s", leaked, result)
		}
	}
}

func TestJSONLBytes_ObjectSkipPolicy(t *testing.T) {
	t.Parallel()
	// Objects with "type":"image", "type":"image_url", or "type":"base64" carry
	// binary payloads, not text; their high-entropy data must be preserved
	// verbatim. The exemption covers "data" only — see
	// TestJSONLBytes_ImagePayloadSkipDoesNotCoverURL. Any other object has its
	// string values redacted normally.
	tests := []struct {
		name      string
		input     string
		preserved bool
	}{
		{
			name:      "image type is skipped",
			input:     `{"type":"image","data":"` + highEntropySecret + `"}`,
			preserved: true,
		},
		{
			name:      "image_url type skips only its data payload",
			input:     `{"type":"image_url","data":"` + highEntropySecret + `"}`,
			preserved: true,
		},
		{
			name:      "image_url url is not a binary payload and is scanned",
			input:     `{"type":"image_url","url":"` + highEntropySecret + `"}`,
			preserved: false,
		},
		{
			name:      "base64 type is skipped",
			input:     `{"type":"base64","data":"` + highEntropySecret + `"}`,
			preserved: true,
		},
		{
			name:      "text type is not skipped",
			input:     `{"type":"text","content":"` + highEntropySecret + `"}`,
			preserved: false,
		},
		{
			name:      "no type field is not skipped",
			input:     `{"content":"` + highEntropySecret + `"}`,
			preserved: false,
		},
		{
			name:      "non-string type is not skipped",
			input:     `{"type":42,"content":"` + highEntropySecret + `"}`,
			preserved: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := redactedString(t, tt.input)
			if tt.preserved {
				if !strings.Contains(got, highEntropySecret) {
					t.Errorf("expected secret to be preserved inside skipped object, got: %s", got)
				}
			} else {
				if strings.Contains(got, highEntropySecret) {
					t.Errorf("expected secret to be redacted, got: %s", got)
				}
				if !strings.Contains(got, "REDACTED") {
					t.Errorf("expected REDACTED in output, got: %s", got)
				}
			}
		})
	}
}

func TestRawLine_FilePaths(t *testing.T) {
	t.Parallel()
	assertRawRedactionCases(t, []stringRedactionCase{
		{
			name:  "temp directory path preserves filenames",
			input: "/tmp/TestE2E_Something3407889464/001/controller.go",
			want:  "/tmp/TestE2E_Something3407889464/001/controller.go",
		},
		{
			name:  "macOS private var folders path",
			input: "/private/var/folders/v4/31cd3cg52_sfrpb1mbtr7q7r0000gn/T/TestE2E_Something/controller",
			want:  "/private/var/folders/v4/31cd3cg52_sfrpb1mbtr7q7r0000gn/T/TestE2E_Something/controller",
		},
		{
			name:  "simple Go file path",
			input: "Reading file: /tmp/test/model.go",
			want:  "Reading file: /tmp/test/model.go",
		},
		{
			name:  "user home directory path",
			input: "/Users/peytonmontei/.claude/projects/something.jsonl",
			want:  "/Users/peytonmontei/.claude/projects/something.jsonl",
		},
		{
			name:  "multiple paths separated by newlines",
			input: "/tmp/test/controller.go\n/tmp/test/model.go\n/tmp/test/view.go",
			want:  "/tmp/test/controller.go\n/tmp/test/model.go\n/tmp/test/view.go",
		},
	})
}

func TestRawLine_JSONEscapeSequences(t *testing.T) {
	t.Parallel()
	assertRawRedactionCases(t, []stringRedactionCase{
		{
			name:  "newline escape not corrupted",
			input: `controller.go\nmodel.go\nview.go`,
			want:  `controller.go\nmodel.go\nview.go`,
		},
		{
			name:  "tab escape not corrupted",
			input: `something.go\tanother.go`,
			want:  `something.go\tanother.go`,
		},
		{
			name:  "backslash escape not corrupted",
			input: `C:\\Users\\test\\file.go`,
			want:  `C:\\Users\\test\\file.go`,
		},
	})
}

func TestRealSecretsStillCaught(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "high entropy API key",
			input: "api_key=" + highEntropySecret,
		},
		{
			name:  "AWS access key (pattern-based)",
			input: "key=AKIAYRWQG5EJLPZLBYNP",
		},
		{
			name:  "GitHub personal access token",
			input: "token=ghp_1234567890abcdefghijklmnopqrstuvwxyzAB",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := redactedField(t, tt.input)
			if !strings.Contains(got, "REDACTED") {
				t.Errorf("redacted %q = %q, expected REDACTED somewhere", tt.input, got)
			}
		})
	}
}

func TestJSONLBytes_PathFieldsPreserved(t *testing.T) {
	t.Parallel()
	// Simulates a real agent log line with path fields that should NOT be redacted
	input := `{"session_id":"ses_37273a1fdffegpYbwUTqEkPsQ0","file_path":"/private/var/folders/v4/31cd3cg52_sfrpb1mbtr7q7r0000gn/T/test/controller.go","cwd":"/private/var/folders/v4/31cd3cg52_sfrpb1mbtr7q7r0000gn/T/test","root":"/private/var/folders/v4/31cd3cg52_sfrpb1mbtr7q7r0000gn/T/test","directory":"/tmp/TestE2E_ExistingFiles","content":"normal text here"}`

	result := redactedString(t, input)

	// Structural fields should be preserved
	mustContain := []string{
		"ses_37273a1fdffegpYbwUTqEkPsQ0", // session_id (skipped by *id rule)
		"/private/var/folders",           // file_path (skipped by path rule)
		"controller.go",                  // filename in file_path
		"/tmp/TestE2E_ExistingFiles",     // directory (skipped by path rule)
	}
	for _, s := range mustContain {
		if !strings.Contains(result, s) {
			t.Errorf("expected %q to be preserved, but result is: %s", s, result)
		}
	}

	// No false positives
	if strings.Contains(result, "REDACTED") {
		t.Errorf("expected no redactions in structural fields, got: %s", result)
	}
}

func TestJSONLBytes_PrettyPrintedJSON_IDsPreserved(t *testing.T) {
	t.Parallel()
	// Simulates OpenCode's pretty-printed JSON export format.
	// High-entropy IDs (like msg_cb99a444f001Ftd3kTVmr8XQHZ with entropy > 4.5,
	// above the redaction threshold) must be preserved. Before the fix,
	// line-by-line processing couldn't parse individual lines of pretty-printed
	// JSON and fell back to entropy-based redaction, corrupting these IDs.
	input := `{
  "info": {
    "id": "ses_309461a8bffeQfY7CYDOUHX6VP",
    "slug": "misty-river",
    "directory": "/tmp/test-repo"
  },
  "messages": [
    {
      "info": {
        "id": "msg_cb99a444f001Ftd3kTVmr8XQHZ",
        "sessionID": "ses_309461a8bffeQfY7CYDOUHX6VP",
        "role": "user"
      },
      "parts": [
        {
          "id": "prt_cb99a443b001GE99vjBG60vHbF",
          "type": "text",
          "text": "hello world"
        }
      ]
    },
    {
      "info": {
        "id": "msg_cb99a444f001Ftd3kTVmr8XQHZ",
        "sessionID": "ses_309461a8bffeQfY7CYDOUHX6VP",
        "role": "assistant"
      },
      "parts": [
        {
          "id": "prt_cb99a6f2e0012koCcOJBSwRBwR",
          "type": "text",
          "text": "hello back"
        },
        {
          "id": "prt_cb99a6f2f001e98CKuwDKU3oWr",
          "type": "tool",
          "tool": "write",
          "callID": "call_abc123",
          "state": {
            "status": "completed",
            "input": {"filePath": "/tmp/test/hello.md"},
            "output": "wrote file",
            "metadata": {"files": [{"filePath": "/tmp/test/hello.md", "relativePath": "hello.md"}]}
          }
        }
      ]
    }
  ]
}`

	result := redactedString(t, input)

	// All IDs must be preserved (they're in "id"/"sessionID" fields which are skipped).
	mustContain := []string{
		"ses_309461a8bffeQfY7CYDOUHX6VP",
		"msg_cb99a444f001Ftd3kTVmr8XQHZ",
		"prt_cb99a443b001GE99vjBG60vHbF",
		"prt_cb99a6f2e0012koCcOJBSwRBwR",
		"prt_cb99a6f2f001e98CKuwDKU3oWr",
	}
	for _, s := range mustContain {
		if !strings.Contains(result, s) {
			t.Errorf("expected ID %q to be preserved, but it was corrupted in result", s)
		}
	}

	// No false positives on structural data.
	if strings.Contains(result, "REDACTED") {
		t.Errorf("expected no redactions in OpenCode export, got redacted content")
	}
}

func TestJSONLBytes_PrettyPrintedJSON_SecretsStillCaught(t *testing.T) {
	t.Parallel()
	// Even in pretty-printed JSON mode, actual secrets in content fields should
	// still be redacted.
	input := `{
  "info": {
    "id": "ses_test123"
  },
  "messages": [
    {
      "info": {
        "id": "msg_test456",
        "role": "assistant"
      },
      "parts": [
        {
          "id": "prt_test789",
          "type": "text",
          "text": "your api key is ` + highEntropySecret + `"
        }
      ]
    }
  ]
}`

	result := redactedString(t, input)

	// Secret in text content should be redacted.
	if strings.Contains(result, highEntropySecret) {
		t.Error("secret in text field was not redacted")
	}
	if !strings.Contains(result, "REDACTED") {
		t.Error("expected REDACTED in output")
	}

	// IDs should still be preserved.
	for _, id := range []string{"ses_test123", "msg_test456", "prt_test789"} {
		if !strings.Contains(result, id) {
			t.Errorf("ID %q should be preserved", id)
		}
	}
}

func TestJSONLBytes_SecretsInContentStillCaught(t *testing.T) {
	t.Parallel()
	// Path fields should be preserved, but secrets in content should be caught
	input := `{"file_path":"/tmp/test.go","content":"api_key=` + highEntropySecret + `"}`

	result := redactedString(t, input)

	// file_path should be preserved
	if !strings.Contains(result, "/tmp/test.go") {
		t.Error("file_path was incorrectly modified")
	}

	// Secret in content should be redacted
	if strings.Contains(result, highEntropySecret) {
		t.Error("secret in content field was not redacted")
	}
	if !strings.Contains(result, "REDACTED") {
		t.Error("expected REDACTED in output")
	}
}

// Pins a known gap: shell shorthand `--password=...` is not redacted because
// no detector matches `--password=` (no DB-prefix, no DSN structure, no URI).
func TestMysqlShellShorthandIsNotRedacted(t *testing.T) {
	t.Parallel()
	assertFieldRedactionCases(t, []stringRedactionCase{
		{
			name:  "mysql cli flag",
			input: "mysql -u svc --password=hunter2 -h db.example.com app",
			want:  "mysql -u svc --password=hunter2 -h db.example.com app",
		},
		{
			name:  "psql cli flag",
			input: "psql --password=hunter2 -U svc -h db.example.com app",
			want:  "psql --password=hunter2 -U svc -h db.example.com app",
		},
	})
}

// Pins f(f(x)) == f(x): once-redacted output must not match any detector on
// a second pass.
func TestRedactionIsIdempotent(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"DATABASE_URL=postgres://svc:hunter2@db.example.com/app",
		"DB_PASSWORD=hunter2",
		`conn=Server=db.example.com;User ID=svc;Password="se;cret;here";Encrypt=true`,
		"jdbc:postgresql://db.example.com:5432/app?user=svc&password=hunter2",
		"my key is " + highEntropySecret + " ok",
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			once := redactedField(t, input)
			twice := redactedField(t, once)
			if once != twice {
				t.Errorf("not idempotent for %q:\n  once:  %q\n  twice: %q", input, once, twice)
			}
		})
	}
}

// Pins keyed-JSON replacement as (key, value) rather than (path, value): a
// shared value under the same key name redacts in every context, not just
// the credential one. Conservative on purpose — flag if changed.
func TestJSONLBytes_CrossContextValueCollision(t *testing.T) {
	t.Parallel()
	input := `{"db":{"host":"db.example.com","user":"svc","password":"shared-secret"},"misc":{"password":"shared-secret"}}`

	result := redactedString(t, input)
	if strings.Contains(result, "shared-secret") {
		t.Errorf("expected shared-secret to be redacted in both contexts, got: %s", result)
	}
	if strings.Count(result, `"password":"REDACTED"`) != 2 {
		t.Errorf("expected both password fields redacted, got: %s", result)
	}
}

func TestJSONLBytes_ObjectUnderProtectedKeyIsScanned(t *testing.T) {
	t.Parallel()
	// A protected key (ends in "id") preserves its own scalar value and an
	// ids array it owns, but an object value re-enters normal evaluation:
	// each field is judged on its own name, so a secret nested under a
	// compound "*_id" key is still masked.
	input := `{"credentials_by_id":{"api_key":"` + highEntropySecret + `","session_id":"keep-` + highEntropySecret + `"}}`
	got := redactedString(t, input)
	if strings.Contains(got, `"api_key":"`+highEntropySecret+`"`) {
		t.Errorf("secret under a compound id key survived: %s", got)
	}
	if !strings.Contains(got, `"session_id":"keep-`+highEntropySecret+`"`) {
		t.Errorf("a genuine nested id field was rewritten: %s", got)
	}
}

func TestJSONLBytes_ImageObjectScansSiblingsButKeepsPayload(t *testing.T) {
	t.Parallel()
	// The binary payload (data/url) of an image object is preserved, but a
	// secret in a sibling field is masked: the skip is scoped to the
	// payload, not the whole object.
	const payload = "sk-ant-api03-DIFFERENTvZ9mZ2vL8nQ5rT1wY4bC7dF0gH3jE6"
	input := `{"type":"image","caption":"` + highEntropySecret + `","source":{"type":"base64","data":"` + payload + `"}}`
	got := redactedString(t, input)
	if strings.Contains(got, highEntropySecret) {
		t.Errorf("sibling secret survived the image skip: %s", got)
	}
	if !strings.Contains(got, payload) {
		t.Errorf("image payload was altered: %s", got)
	}
}

// TestJSONLBytes_ImagePayloadSkipDoesNotCoverURL pins the binary-payload
// exemption to "data". A url is an ordinary short string and is the one
// shape that routinely carries a credential; exempting it too — as this
// did until 2026-08-26 — let any object naming itself image_url hand a
// userinfo password or a signed query past all seven layers and out to
// the service unmasked. "type" is free text a model or an MCP tool
// writes into a recorded body, so that is not a corner.
func TestJSONLBytes_ImagePayloadSkipDoesNotCoverURL(t *testing.T) {
	t.Parallel()
	const payload = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk"
	const signedURL = "https://s3.example.com/shot.png?X-Amz-Credential=AKIAIOSFODNN7EXAMPLE&X-Amz-Signature=b4a1c9e77d2f5061"
	const userinfoURL = "https://svc:hunter2Passw0rd@images.example.com/shot.png"
	for _, typeName := range []string{"image", "image_url", "base64"} {
		t.Run(typeName, func(t *testing.T) {
			t.Parallel()
			in := `{"type":"` + typeName + `","url":"` + signedURL +
				`","thumbnail_url":"` + userinfoURL + `","data":"` + payload + `"}`
			got := redactedString(t, in)
			if strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") || strings.Contains(got, "b4a1c9e77d2f5061") {
				t.Errorf("signed url survived under type %q: %s", typeName, got)
			}
			if strings.Contains(got, "hunter2Passw0rd") {
				t.Errorf("userinfo password survived under type %q: %s", typeName, got)
			}
			if !strings.Contains(got, payload) {
				t.Errorf("binary data payload was altered under type %q: %s", typeName, got)
			}
		})
	}
}

// TestJSONLBytes_ImageLookalikeTypeIsScanned pins the object skip policy
// to exact type names: "type" is free text a model or an MCP tool
// writes, and a prefix match on "image" let any object naming itself
// image_metadata carry url and data past every detection layer.
func TestJSONLBytes_ImageLookalikeTypeIsScanned(t *testing.T) {
	t.Parallel()
	for _, typeName := range []string{"image_metadata", "image_reference", "images", "imagery"} {
		t.Run(typeName, func(t *testing.T) {
			t.Parallel()
			in := `{"type":"` + typeName + `","url":"` + highEntropySecret + `","data":"` + highEntropySecret + `"}`
			got := redactedString(t, in)
			if strings.Contains(got, highEntropySecret) {
				t.Errorf("secret survived under type %q: %s", typeName, got)
			}
		})
	}
}

// TestJSONLBytes_CredentialKeyMasksWholeValue pins the credential-key
// rule: a key naming its value a password masks the value whole. It used
// to apply only when nothing else had matched, so a password holding one
// recognizable high-entropy token kept its remaining plaintext.
func TestJSONLBytes_CredentialKeyMasksWholeValue(t *testing.T) {
	t.Parallel()
	// The segment before the slash is high-entropy enough for the entropy
	// layer on its own; "tail" is below the pattern's length floor, so
	// before the fix the result was "REDACTED/tail".
	const value = "aB3dEfGh1JkLmN0pQrStUvWxYz2/tail"
	got := redactedString(t, `{"db_password":"`+value+`"}`)
	if want := `{"db_password":"REDACTED"}`; got != want {
		t.Errorf("redacted = %s, want %s", got, want)
	}
}

// TestDatabaseURLPasswordWithPercent pins that a query-string password
// is found in the raw text. url.Query drops any pair whose value holds
// an invalid percent escape, which made the whole URL read as
// password-free and leave the machine unmasked.
func TestDatabaseURLPasswordWithPercent(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"postgresql://db.example.com/app?user=svc&password=p%ssw0rd",
		"mysql://db.example.com/app?password=100%sure&user=svc",
	} {
		got := redactedString(t, in)
		if strings.Contains(got, "ssw0rd") || strings.Contains(got, "sure") {
			t.Errorf("database URL password survived: %q -> %q", in, got)
		}
	}
}
