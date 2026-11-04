package redact_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/redact"
)

func TestMasker_RecordsThatShareValuesMaskAsEachRecordDoesWithoutAMemo(t *testing.T) {
	t.Parallel()
	history := strings.Repeat("the agent read the config and ran the tests ", 40) + "api_key=" + highEntropySecret
	document := `{"database":{"host":"db.internal.example.com","user":"app","password":"hunter2hunter"},"notes":"` + history + `"}`
	records := []any{
		map[string]any{"messages": []any{history}, "input": map[string]any{"db_password": "hunter2hunter"}},
		map[string]any{"messages": []any{history, document}, "note": "hunter2hunter", "id": highEntropySecret},
		map[string]any{"messages": []any{history, document, "write to dev@example.com"}, "text": highEntropySecret, "args": []any{"hunter2hunter"}},
		map[string]any{"messages": []any{history, document, "write to dev@example.com"}, "tool_use_id": "dev@example.com", "password": highEntropySecret},
	}
	var lines []string
	for line := range strings.SplitSeq(string(generateBenchmarkJSONL(t, 60)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}

	m := redact.NewMasker()
	check := func(record []byte) {
		t.Helper()
		got, err := m.JSONLBytes(record)
		if err != nil {
			t.Fatal(err)
		}
		want, err := redact.MaskWithoutMemo(record)
		if err != nil {
			t.Fatal(err)
		}
		if string(got.Bytes()) != string(want.Bytes()) {
			t.Errorf("masked with the memo differs from masked without it:\n got %.300s\nwant %.300s", got.Bytes(), want.Bytes())
		}
	}
	for _, r := range records {
		record, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		check(record)
	}
	for _, line := range lines {
		check([]byte(line))
	}
	check([]byte(strings.Join(lines, "\n")))
}
