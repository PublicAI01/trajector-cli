package drift

import (
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/redact"
)

func TestScanCountsAssistantLinesMissingAResponseFieldOnceOneIsRequired(t *testing.T) {
	requiredResponseFields = []string{"model"}
	t.Cleanup(func() { requiredResponseFields = nil })

	s, err := Scan([]byte(`{"type":"assistant","message":{"id":"msg_1","model":"a-model"}}`+"\n"+
		`{"type":"assistant","message":{"id":"msg_2"}}`+"\n"), redact.SessionLocation{})
	if err != nil {
		t.Fatal(err)
	}
	if s.AssistantLines != 2 || s.AssistantLinesMissingResponseFields != 1 {
		t.Errorf("assistant lines = %d, missing a response field = %d; want 2 and 1", s.AssistantLines, s.AssistantLinesMissingResponseFields)
	}
	if !s.Alerts() || s.Stop() {
		t.Errorf("Alerts() = %v, Stop() = %v; want the line stored and the count shown", s.Alerts(), s.Stop())
	}
}
