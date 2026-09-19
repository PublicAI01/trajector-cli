package envelope_test

import (
	"bytes"
	"os"
	"slices"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
)

func TestRestatedRewritesOnlyTheSchemaVersionToken(t *testing.T) {
	current, err := os.ReadFile("testdata/rawcall.json")
	if err != nil {
		t.Fatal(err)
	}
	older := bytes.Replace(current, []byte(`"schema_version":"`+envelope.SchemaVersion+`"`), []byte(`"schema_version":"1"`), 1)
	if bytes.Equal(older, current) {
		t.Fatal("test setup: the golden record does not open with the current schema version")
	}

	env, err := envelope.Parse(older)
	if err != nil {
		t.Fatal(err)
	}
	restated, err := env.Restated()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restated, current) {
		t.Errorf("restated record differs from the same record stored at the current version:\n got %s\nwant %s", restated, current)
	}

	env, err = envelope.Parse(current)
	if err != nil {
		t.Fatal(err)
	}
	same, err := env.Restated()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(same, current) {
		t.Errorf("a record at the current version was rewritten:\n got %s\nwant %s", same, current)
	}
}

func TestRestatedRefusesARecordThatDoesNotOpenWithItsVersion(t *testing.T) {
	current, err := os.ReadFile("testdata/rawcall.json")
	if err != nil {
		t.Fatal(err)
	}
	reordered := bytes.Replace(current, []byte(`{"schema_version":"`+envelope.SchemaVersion+`","source":"proxy",`), []byte(`{"source":"proxy","schema_version":"1",`), 1)
	env, err := envelope.Parse(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Restated(); err == nil {
		t.Error("a record whose first member is not its schema version was restated")
	}
}

func TestHintsAreTheProviderHeadersTheRecordCarries(t *testing.T) {
	current, err := os.ReadFile("testdata/rawcall.json")
	if err != nil {
		t.Fatal(err)
	}
	env, err := envelope.Parse(current)
	if err != nil {
		t.Fatal(err)
	}
	hints := env.Hints()
	if hints.AnthropicVersion != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want the header the record stored", hints.AnthropicVersion)
	}
	if want := []string{"beta-a", "beta-b"}; !slices.Equal(hints.AnthropicBeta, want) {
		t.Errorf("anthropic-beta = %q, want %q", hints.AnthropicBeta, want)
	}
}

func TestValidRequestIDAdmitsOnlyIdsThatStayInsideADayDirectory(t *testing.T) {
	for _, c := range []struct {
		id   string
		want bool
	}{
		{"msg_01ABCDEF", true},
		{"a.b-c_d", true},
		{"local-0123abcd", true},
		{"", false},
		{"../escape", false},
		{"-leading", false},
		{"a/b", false},
		{"with space", false},
	} {
		if got := envelope.ValidRequestID(c.id); got != c.want {
			t.Errorf("ValidRequestID(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

func TestRestatedSetsTheCurrentSchemaVersionOnEveryRecordKind(t *testing.T) {
	capture := envelope.Capture{Timestamp: "2026-08-01T12:30:45Z", ProjectIDHash: "0011aabb"}

	seg := envelope.NewSegment("sid", "sid.jsonl", 0, capture, "{}\n")
	seg.SchemaVersion = "1"
	if got := seg.Restated().SchemaVersion; got != envelope.SchemaVersion {
		t.Errorf("segment restated to %q, want %q", got, envelope.SchemaVersion)
	}

	meta, err := envelope.NewMetaSnapshot("sid", "subagents/agent-0000.meta.json", capture, []byte(`{"agentId":"0000"}`))
	if err != nil {
		t.Fatal(err)
	}
	meta.SchemaVersion = "1"
	if got := meta.Restated().SchemaVersion; got != envelope.SchemaVersion {
		t.Errorf("snapshot restated to %q, want %q", got, envelope.SchemaVersion)
	}

	git := envelope.NewGitSnapshot("sid", "SessionStart", envelope.TriggerSessionStart, capture, "main",
		"d0cf90f327430f11f8a68493a58f402fa11d7c9e", envelope.CommitOrNone(""), envelope.CommitOrNone(""), nil)
	git.SchemaVersion = "1"
	if got := git.Restated().SchemaVersion; got != envelope.SchemaVersion {
		t.Errorf("git snapshot restated to %q, want %q", got, envelope.SchemaVersion)
	}
}
