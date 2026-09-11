package batch_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/batch"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

func transcriptCapture(at time.Time) envelope.TranscriptCapture {
	return envelope.TranscriptCapture{
		ClientVersion: "test",
		Timestamp:     at.UTC().Format(time.RFC3339Nano),
		ProjectIDHash: "hash-p1",
		Injection:     envelope.InjectionProxy,
	}
}

func TestTheEnvelopeSerializesEveryContractFieldInOrder(t *testing.T) {
	rc := simpleRawcall(t, "req-1", "session-a", buildTime)
	seg := storedSegment(t, "sess-1", "", 0, buildTime, "{}\n")
	snap := storedSnapshot(t, "sess-1", "subagents/agent-x.meta.json", buildTime.Add(time.Minute), `{"agentId":"x"}`)

	b, refused, err := batch.Build("batch-42", buildTime, "1.2.3", batch.Contents{
		Rawcalls:       []spool.Rawcall{rc},
		SessionRecords: []spool.Record{seg, snap},
	}, batch.Run{RecordedToday: 3})
	if err != nil || len(refused) != 0 {
		t.Fatalf("Build: %v, refused %+v", err, refused)
	}
	ix := parseIndex(t, b)
	if len(ix.Records) != 3 {
		t.Fatalf("index = %+v", ix.Records)
	}
	items := ix.Records
	want := fmt.Sprintf(`{"schema_version":"2","batch_id":"batch-42","client_version":"1.2.3","created_at":"2026-08-03T10:00:00Z","compression":"zstd","records_size":%d,"records":[`, ix.RecordsSize) +
		fmt.Sprintf(`{"record_id":"req-1","source":"proxy","project_id_hash":"hash-p1","upstream_origin":"official","endpoint":"/v1/messages","timestamp":"2026-08-03T10:00:00Z","offset":%d,"size":%d},`, items[0].Offset, items[0].Size) +
		fmt.Sprintf(`{"record_id":%q,"source":"transcript","project_id_hash":"hash-p1","timestamp":"2026-08-03T10:00:00Z","offset":%d,"size":%d},`, seg.ID, items[1].Offset, items[1].Size) +
		fmt.Sprintf(`{"record_id":%q,"source":"transcript","project_id_hash":"hash-p1","timestamp":"2026-08-03T10:01:00Z","offset":%d,"size":%d}],`, snap.ID, items[2].Offset, items[2].Size) +
		`"run":{"recorded_today":3,"sse_degraded_today":0,"captures_dropped":0,"spool_usage_bytes":0,"spool_quota_bytes":0}}`
	if string(b.Envelope) != want {
		t.Errorf("envelope:\n got %s\nwant %s", b.Envelope, want)
	}

	var offset int64
	for i, item := range items {
		if item.Offset != offset || item.Size <= 0 {
			t.Errorf("record %d: offset %d size %d, want offset %d", i, item.Offset, item.Size, offset)
		}
		offset += item.Size
	}
	if offset != ix.RecordsSize {
		t.Errorf("records_size = %d, items cover %d bytes", ix.RecordsSize, offset)
	}

	again, err := ix.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(b.Envelope) {
		t.Errorf("round trip changed the envelope:\n got %s\nwant %s", again, b.Envelope)
	}
}

func TestTranscriptItemsCarryNoUpstreamFields(t *testing.T) {
	seg := storedSegment(t, "sess-1", "", 0, buildTime, "{}\n")
	b, refused, err := batch.Build("batch-1", buildTime, "test", batch.Contents{SessionRecords: []spool.Record{seg}}, batch.Run{})
	if err != nil || len(refused) != 0 {
		t.Fatalf("Build: %v, refused %+v", err, refused)
	}
	for _, field := range []string{"upstream_origin", "endpoint", "garbled"} {
		if strings.Contains(string(b.Envelope), field) {
			t.Errorf("the envelope of a transcript-only batch carries %q: %s", field, b.Envelope)
		}
	}
}

func TestGarbledRawcallIsMarkedInTheIndex(t *testing.T) {
	rc := storedRawcall(t, "req-g", "s", "hash-p1", `{"model":"m"}`, "not json", buildTime)
	b, refused, err := batch.Build("batch-1", buildTime, "test", rawcalls(rc), batch.Run{})
	if err != nil || len(refused) != 0 {
		t.Fatalf("Build: %v, refused %+v", err, refused)
	}
	if !strings.Contains(string(b.Envelope), `"garbled":true`) {
		t.Errorf("garbled rawcall indexed as %s", b.Envelope)
	}
}

func TestParseIndexV2RefusesOtherVersions(t *testing.T) {
	v1 := []byte(`{"schema_version":"1","batch_id":"batch-1","client_version":"test","created_at":"2026-08-03T10:00:00Z","compression":"zstd","records_size":0,"records":[],"run":{}}`)
	if _, err := batch.ParseIndexV2(v1); err == nil {
		t.Error("a schema_version 1 envelope parsed as 2")
	}
	if _, err := batch.ParseIndexV2([]byte("{")); err == nil {
		t.Error("unreadable bytes parsed")
	}
}

func TestBuildEmitsSchemaVersion2(t *testing.T) {
	rc := simpleRawcall(t, "req-1", "session-a", buildTime)
	b, _, err := batch.Build("batch-1", buildTime, "test", rawcalls(rc), batch.Run{})
	if err != nil {
		t.Fatal(err)
	}
	ix, err := batch.ParseIndexV2(b.Envelope)
	if err != nil {
		t.Fatalf("Build's envelope is not a schema_version 2 index: %v", err)
	}
	if ix.SchemaVersion != "2" || ix.Records[0].RecordID != "req-1" || ix.Records[0].Source != "proxy" {
		t.Errorf("Build emitted %s", b.Envelope)
	}
	if strings.Contains(string(b.Envelope), "request_id") {
		t.Errorf("Build emitted the schema_version 1 key: %s", b.Envelope)
	}
}
