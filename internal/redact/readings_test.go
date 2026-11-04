package redact_test

import (
	"encoding/json"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/redact"
)

func TestJSONLBytesKeepsFieldPoliciesIndependentForSharedText(t *testing.T) {
	input := map[string]string{
		"session_id": highEntropySecret,
		"content":    highEntropySecret,
		"contact_id": "jane@example.com",
		"note":       "jane@example.com",
		"service_id": "postgres://app:hunter2hunter@db.example.com/database",
		"message":    "postgres://app:hunter2hunter@db.example.com/database",
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	masked, err := redact.JSONLBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(masked.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"session_id": highEntropySecret,
		"content":    "REDACTED",
		"contact_id": "jane@example.com",
		"note":       "[REDACTED_EMAIL]",
		"service_id": "REDACTED",
		"message":    "REDACTED",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("field %q = %q, want %q", key, got[key], value)
		}
	}
}
