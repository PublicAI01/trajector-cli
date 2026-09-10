package batch_test

import (
	"encoding/json"
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

func TestIndexV2LaysOutRecordsContiguouslyAndNamesEachSource(t *testing.T) {
	rc := simpleRawcall(t, "req-1", "session-a", buildTime)
	env, err := envelope.Parse(rc.Data)
	if err != nil {
		t.Fatal(err)
	}
	seg := envelope.NewSegment("sess-1", "", 0, transcriptCapture(buildTime), "{}\n")
	snap, err := envelope.NewMetaSnapshot("sess-1", "subagents/agent-x.meta.json", transcriptCapture(buildTime.Add(time.Minute)), []byte(`{"agentId":"x"}`))
	if err != nil {
		t.Fatal(err)
	}

	ix := batch.NewIndexV2("batch-42", buildTime, "1.2.3", batch.Run{RecordedToday: 3})
	ix.Add(batch.RawcallItem(env), 100)
	ix.Add(batch.SegmentItem(seg), 20)
	ix.Add(batch.MetaSnapshotItem(snap), 30)

	data, err := ix.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":"2","batch_id":"batch-42","client_version":"1.2.3","created_at":"2026-08-03T10:00:00Z","compression":"zstd","records_size":150,"records":[` +
		`{"record_id":"req-1","source":"proxy","project_id_hash":"hash-p1","upstream_origin":"official","endpoint":"/v1/messages","timestamp":"2026-08-03T10:00:00Z","offset":0,"size":100},` +
		`{"record_id":"` + seg.RecordID + `","source":"transcript","project_id_hash":"hash-p1","timestamp":"2026-08-03T10:00:00Z","offset":100,"size":20},` +
		`{"record_id":"` + snap.RecordID + `","source":"transcript","project_id_hash":"hash-p1","timestamp":"2026-08-03T10:01:00Z","offset":120,"size":30}],` +
		`"run":{"recorded_today":3,"sse_degraded_today":0,"captures_dropped":0,"spool_usage_bytes":0,"spool_quota_bytes":0}}`
	if string(data) != want {
		t.Errorf("envelope:\n got %s\nwant %s", data, want)
	}

	read, err := batch.ParseIndexV2(data)
	if err != nil {
		t.Fatal(err)
	}
	again, err := read.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Errorf("round trip changed the envelope:\n got %s\nwant %s", again, data)
	}
}

func TestIndexV2TranscriptItemsCarryNoUpstreamFields(t *testing.T) {
	seg := envelope.NewSegment("sess-1", "", 0, transcriptCapture(buildTime), "{}\n")
	item, err := json.Marshal(batch.SegmentItem(seg))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"upstream_origin", "endpoint", "garbled"} {
		if strings.Contains(string(item), field) {
			t.Errorf("transcript item carries %q: %s", field, item)
		}
	}
}

func TestIndexV2GarbledRawcallIsMarkedInTheIndex(t *testing.T) {
	rc := storedRawcall(t, "req-g", "s", "hash-p1", `{"model":"m"}`, "not json", buildTime)
	env, err := envelope.Parse(rc.Data)
	if err != nil {
		t.Fatal(err)
	}
	item, err := json.Marshal(batch.RawcallItem(env))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(item), `"garbled":true`) {
		t.Errorf("garbled rawcall indexed as %s", item)
	}
}

func TestParseIndexV2RefusesOtherVersions(t *testing.T) {
	rc := simpleRawcall(t, "req-1", "session-a", buildTime)
	v1, _, err := batch.Build("batch-1", buildTime, "test", []spool.Rawcall{rc}, batch.Run{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := batch.ParseIndexV2(v1.Envelope); err == nil {
		t.Error("a schema_version 1 envelope parsed as 2")
	}
	if _, err := batch.ParseIndexV2([]byte("{")); err == nil {
		t.Error("unreadable bytes parsed")
	}
}

func TestBuildStillEmitsSchemaVersion1(t *testing.T) {
	rc := simpleRawcall(t, "req-1", "session-a", buildTime)
	b, _, err := batch.Build("batch-1", buildTime, "test", []spool.Rawcall{rc}, batch.Run{})
	if err != nil {
		t.Fatal(err)
	}
	env := parseEnvelope(t, b)
	if env.SchemaVersion != "1" || env.Records[0].RequestID != "req-1" {
		t.Errorf("Build changed the upload envelope: %s", b.Envelope)
	}
	if strings.Contains(string(b.Envelope), "record_id") || strings.Contains(string(b.Envelope), `"source"`) {
		t.Errorf("Build emitted schema_version 2 fields: %s", b.Envelope)
	}
}
