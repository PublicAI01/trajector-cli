package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func narrowPII(t *testing.T, categories ...piiCategory) {
	t.Helper()
	configurePII(categories...)
	t.Cleanup(func() { configurePII(defaultPIICategories...) })
}

func maskedContent(t *testing.T, s string) string {
	t.Helper()
	doc, err := json.Marshal(map[string]string{"content": s})
	if err != nil {
		t.Fatal(err)
	}
	rb, err := JSONLBytes(doc)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if err := json.Unmarshal(rb.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out["content"]
}

func TestNarrowingToOneCategoryLeavesTheOtherShapeAlone(t *testing.T) {
	narrowPII(t, piiEmail)

	got := maskedContent(t, "email user@example.com phone 555-123-4567")
	if !strings.Contains(got, "[REDACTED_EMAIL]") {
		t.Errorf("email was not masked: %q", got)
	}
	if !strings.Contains(got, "555-123-4567") {
		t.Errorf("phone was masked outside the selected category: %q", got)
	}
}

func TestNarrowingToNoCategoryMasksNoPII(t *testing.T) {
	narrowPII(t)

	input := "contact user@example.com and call 555-123-4567"
	if got := maskedContent(t, input); got != input {
		t.Errorf("got %q, want %q", got, input)
	}
}

func TestTheDefaultCategoriesMaskEmailAndPhone(t *testing.T) {
	narrowPII(t, defaultPIICategories...)

	if got := maskedContent(t, "user@example.com"); got != "[REDACTED_EMAIL]" {
		t.Errorf("email token = %q", got)
	}
	if got := maskedContent(t, "call 555-123-4567"); got != "call [REDACTED_PHONE]" {
		t.Errorf("phone token = %q", got)
	}
}
