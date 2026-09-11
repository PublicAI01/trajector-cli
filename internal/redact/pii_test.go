package redact_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/redact"
)

func TestPII_EmailDetection(t *testing.T) {
	masked := []string{
		"user@example.com",
		"user+tag@domain.co.uk",
		"first.last@company.org",
		"a@b.com",
	}
	for _, email := range masked {
		got := redactedField(t, "contact "+email+" for info")
		if strings.Contains(got, email) {
			t.Errorf("email %q should be masked, got %q", email, got)
		}
		if !strings.Contains(got, "[REDACTED_EMAIL]") {
			t.Errorf("expected [REDACTED_EMAIL] for %q, got %q", email, got)
		}
	}

	unchanged := []string{
		"not an email",
		"@missing.local",
		"missing@",
		"no-at-sign-here",
	}
	for _, s := range unchanged {
		if got := redactedField(t, s); got != s {
			t.Errorf("non-email %q should pass through unchanged, got %q", s, got)
		}
	}
}

func TestPII_PhoneDetection(t *testing.T) {
	masked := []string{
		"555-123-4567",
		"(555) 123-4567",
		"+1-555-123-4567",
		"+1.555.123.4567",
		"1-555-123-4567",
		"555 123 4567",
	}
	for _, phone := range masked {
		got := redactedField(t, "call "+phone+" now")
		if strings.Contains(got, phone) {
			t.Errorf("phone %q should be masked, got %q", phone, got)
		}
		if !strings.Contains(got, "[REDACTED_PHONE]") {
			t.Errorf("expected [REDACTED_PHONE] for %q, got %q", phone, got)
		}
	}

	unchanged := []string{
		"42",
		"12345",
		"not a phone",
		"1.234.567.8901",   // version-like dotted decimal
		"192.168.001.0001", // IP-like dotted decimal
		"555.123.4567",     // bare dots without +1 prefix (intentionally rejected)
	}
	for _, s := range unchanged {
		if got := redactedField(t, s); got != s {
			t.Errorf("non-phone %q should pass through unchanged, got %q", s, got)
		}
	}
}

func TestPII_MultipleEmails(t *testing.T) {
	got := redactedField(t, "a@b.com and c@d.org")
	want := "[REDACTED_EMAIL] and [REDACTED_EMAIL]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPII_ReplacementTokenFormat(t *testing.T) {
	if got := redactedField(t, "user@example.com"); got != "[REDACTED_EMAIL]" {
		t.Errorf("email token = %q, want [REDACTED_EMAIL]", got)
	}
	if got := redactedField(t, "call 555-123-4567"); got != "call [REDACTED_PHONE]" {
		t.Errorf("phone token = %q, want call [REDACTED_PHONE]", got)
	}
	// Secrets keep the bare REDACTED placeholder beside the PII tokens.
	if got := redactedField(t, "my key is "+highEntropySecret+" ok"); got != "my key is REDACTED ok" {
		t.Errorf("secret token = %q, want my key is REDACTED ok", got)
	}
}

func TestPII_SecretAndPIICoexist(t *testing.T) {
	input := "key=" + highEntropySecret + " and user@example.com"
	got := redactedField(t, input)
	if strings.Contains(got, highEntropySecret) {
		t.Errorf("secret should be redacted, got %q", got)
	}
	if strings.Contains(got, "user@example.com") {
		t.Errorf("email should be redacted, got %q", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Errorf("expected at least one REDACTED token, got %q", got)
	}
	if !strings.Contains(got, "[REDACTED_EMAIL]") {
		t.Errorf("expected [REDACTED_EMAIL] token, got %q", got)
	}
}

// TestPII_JSONLSessionLineMasked drives a realistic session line through
// JSONLBytes: email and phone in a content leaf must be masked with
// their typed tokens.
func TestPII_JSONLSessionLineMasked(t *testing.T) {
	input := `{"type":"user","content":"reach me at jane.doe@example.com or 555-123-4567"}`
	rb, err := redact.JSONLBytes([]byte(input))
	if err != nil {
		t.Fatalf("JSONLBytes: %v", err)
	}
	got := string(rb.Bytes())
	if strings.Contains(got, "jane.doe@example.com") {
		t.Errorf("email should be masked in JSONL, got %q", got)
	}
	if strings.Contains(got, "555-123-4567") {
		t.Errorf("phone should be masked in JSONL, got %q", got)
	}
	if !strings.Contains(got, "[REDACTED_EMAIL]") {
		t.Errorf("expected [REDACTED_EMAIL] in JSONL output, got %q", got)
	}
	if !strings.Contains(got, "[REDACTED_PHONE]") {
		t.Errorf("expected [REDACTED_PHONE] in JSONL output, got %q", got)
	}
}

func TestPII_AllowlistedEmailsPreserved(t *testing.T) {
	// These emails appear constantly in coding sessions (git authors, bot
	// accounts) and are non-sensitive public metadata.
	allowlisted := []string{
		"noreply@github.com",
		"user@users.noreply.github.com",
		"dependabot@users.noreply.github.com",
		"actions@github.com",
		"someone@noreply.github.com",
		"Noreply@GitHub.com", // case-insensitive
	}
	for _, email := range allowlisted {
		input := "from " + email + " to"
		if got := redactedField(t, input); got != input {
			t.Errorf("allowlisted email %q should NOT be masked, got %q", email, got)
		}
	}

	// Regular emails should still be masked.
	got := redactedField(t, "contact user@example.com for info")
	if !strings.Contains(got, "[REDACTED_EMAIL]") {
		t.Errorf("non-allowlisted email should still be masked, got %q", got)
	}
}

func TestPII_GitAuthorNoreplyPreserved(t *testing.T) {
	// Simulates git log output — noreply addresses should not be redacted.
	input := "Author: Bot <noreply@github.com>\nCo-Authored-By: User <user@users.noreply.github.com>"
	if got := redactedString(t, input); got != input {
		t.Errorf("noreply emails in git author lines should NOT be masked, got %q", got)
	}
}

func TestPIIEnabled_FilePathsStillPreserved(t *testing.T) {
	paths := []string{
		"/tmp/TestE2E_Something3407889464/001/controller.go",
		"/private/var/folders/v4/31cd3cg52_sfrpb1mbtr7q7r0000gn/T/TestE2E_Something/controller",
		"/Users/peytonmontei/.claude/projects/something.jsonl",
		"/tmp/test/controller.go\n/tmp/test/model.go\n/tmp/test/view.go",
	}
	for _, p := range paths {
		got := redactedString(t, p)
		if got != p {
			t.Errorf("file path should NOT be redacted with PII enabled\n  input: %q\n  got:   %q", p, got)
		}
	}
}

func TestPIIEnabled_JSONEscapesStillPreserved(t *testing.T) {
	tests := []string{
		`controller.go\nmodel.go\nview.go`,
		`something.go\tanother.go`,
		`C:\\Users\\test\\file.go`,
	}
	for _, input := range tests {
		got := redactedString(t, input)
		if got != input {
			t.Errorf("JSON escape should NOT be corrupted with PII enabled\n  input: %q\n  got:   %q", input, got)
		}
	}
}

func TestPIIEnabled_JSONLPathFieldsStillSkipped(t *testing.T) {
	input := `{"file_path":"/private/var/folders/v4/31cd3cg52_sfrpb1mbtr7q7r0000gn/T/test/controller.go","cwd":"/private/var/folders/v4/31cd3cg52_sfrpb1mbtr7q7r0000gn/T/test","content":"normal text here"}`
	got := redactedString(t, input)
	if strings.Contains(got, "REDACTED") {
		t.Errorf("JSONL path fields should NOT be redacted with PII enabled, got: %s", got)
	}
}

func TestPIIEnabled_SecretPatternExcludesSlash(t *testing.T) {
	// This path was being redacted when / was in the entropy-layer pattern.
	input := "/private/var/folders/v4/31cd3cg52_sfrpb1mbtr7q7r0000gn/T/TestE2E_Something/controller"
	got := redactedString(t, input)
	if got != input {
		t.Errorf("path with slashes should NOT be redacted\n  input: %q\n  got:   %q", input, got)
	}
}

func TestPII_JSONLSkippedFieldWithEmail(t *testing.T) {
	// Email in file_path field should NOT be redacted (field is skipped).
	// Email in content field SHOULD be redacted.
	input := `{"file_path":"user@example.com/project/file.go","content":"contact admin@test.org"}`
	got := redactedString(t, input)
	if !strings.Contains(got, "user@example.com") {
		t.Errorf("email in file_path should NOT be redacted, got: %s", got)
	}
	if strings.Contains(got, "admin@test.org") {
		t.Errorf("email in content should be redacted, got: %s", got)
	}
}
