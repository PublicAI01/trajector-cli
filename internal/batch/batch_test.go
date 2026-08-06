package batch_test

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/PublicAI01/trajector-cli/internal/batch"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

const fakeSecret = "sk-test-fake-xK9mZ2vL8nQ5rT1wY4bC7dF0gH3jE6pA"

const fakeSignature = "EqQBCkgIBBgCIkDunT5RmZFPqBWEcTbEK4DZWWL9zGnDx0M0vGRnHkV6wZQx"

var buildTime = time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)

func storedRawcall(t *testing.T, id, sessionKey, projectHash, requestBody, responseBody string, at time.Time) spool.Rawcall {
	t.Helper()
	env, err := envelope.Record(envelope.Observation{
		Provider:          "anthropic",
		Endpoint:          "/v1/messages",
		HTTPStatus:        200,
		ClientVersion:     "test",
		ProjectIDHash:     projectHash,
		At:                at,
		Upstream:          "https://api.anthropic.com",
		OfficialUpstream:  "https://api.anthropic.com",
		Request:           []byte(requestBody),
		RequestComplete:   true,
		Response:          []byte(responseBody),
		ResponseComplete:  true,
		ContentType:       "application/json",
		UpstreamRequestID: id,
	})
	if err != nil {
		t.Fatalf("recording fixture rawcall: %v", err)
	}
	return spool.Rawcall{
		RequestID:  env.RequestID(),
		SessionKey: sessionKey,
		Timestamp:  at,
		Size:       int64(len(env.Bytes())),
		Data:       env.Bytes(),
	}
}

func simpleRawcall(t *testing.T, id, sessionKey string, at time.Time) spool.Rawcall {
	t.Helper()
	return storedRawcall(t, id, sessionKey, "hash-p1",
		`{"model":"m","messages":[{"role":"user","content":"hello"}]}`,
		`{"id":"`+id+`","type":"message"}`, at)
}

func decompress(t *testing.T, data []byte) []byte {
	t.Helper()
	zr, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("opening zstd stream: %v", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompressing records: %v", err)
	}
	return out
}

type wireEnvelope struct {
	SchemaVersion string `json:"schema_version"`
	BatchID       string `json:"batch_id"`
	ClientVersion string `json:"client_version"`
	CreatedAt     string `json:"created_at"`
	Compression   string `json:"compression"`
	RecordsSize   int64  `json:"records_size"`
	Records       []struct {
		RequestID      string `json:"request_id"`
		ProjectIDHash  string `json:"project_id_hash"`
		UpstreamOrigin string `json:"upstream_origin"`
		Endpoint       string `json:"endpoint"`
		Timestamp      string `json:"timestamp"`
		Garbled        bool   `json:"garbled"`
		Offset         int64  `json:"offset"`
		Size           int64  `json:"size"`
	} `json:"records"`
	Run struct {
		RecordedToday    int   `json:"recorded_today"`
		SSEDegradedToday int   `json:"sse_degraded_today"`
		CapturesDropped  int   `json:"captures_dropped"`
		SpoolUsageBytes  int64 `json:"spool_usage_bytes"`
		SpoolQuotaBytes  int64 `json:"spool_quota_bytes"`
	} `json:"run"`
}

func parseEnvelope(t *testing.T, b batch.Batch) wireEnvelope {
	t.Helper()
	var env wireEnvelope
	if err := json.Unmarshal(b.Envelope, &env); err != nil {
		t.Fatalf("batch envelope is not valid JSON: %v", err)
	}
	return env
}

func TestBuildLaysOutSameSessionRecordsAdjacently(t *testing.T) {
	rawcalls := []spool.Rawcall{
		simpleRawcall(t, "req-a1", "session-a", buildTime),
		simpleRawcall(t, "req-b1", "session-b", buildTime.Add(1*time.Second)),
		simpleRawcall(t, "req-a2", "session-a", buildTime.Add(2*time.Second)),
		simpleRawcall(t, "req-n1", "", buildTime.Add(3*time.Second)),
		simpleRawcall(t, "req-b2", "session-b", buildTime.Add(4*time.Second)),
	}
	b, err := batch.Build("batch-1", buildTime, "test", rawcalls, batch.Run{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	env := parseEnvelope(t, b)
	var order []string
	for _, r := range env.Records {
		order = append(order, r.RequestID)
	}
	want := []string{"req-a1", "req-a2", "req-b1", "req-b2", "req-n1"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("record order = %v, want %v", order, want)
	}
}

func TestBuildRecordsRoundTripThroughTheIndex(t *testing.T) {
	rawcalls := []spool.Rawcall{
		simpleRawcall(t, "req-1", "session-a", buildTime),
		simpleRawcall(t, "req-2", "session-a", buildTime.Add(time.Second)),
		simpleRawcall(t, "req-3", "", buildTime.Add(2*time.Second)),
	}
	b, err := batch.Build("batch-1", buildTime, "test", rawcalls, batch.Run{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	env := parseEnvelope(t, b)
	stream := decompress(t, b.Records)
	if int64(len(stream)) != env.RecordsSize {
		t.Fatalf("decompressed %d bytes, envelope says %d", len(stream), env.RecordsSize)
	}
	if len(env.Records) != len(rawcalls) {
		t.Fatalf("index has %d records, want %d", len(env.Records), len(rawcalls))
	}
	for _, r := range env.Records {
		record := stream[r.Offset : r.Offset+r.Size]
		if !json.Valid(record) {
			t.Errorf("record %s is not valid JSON after packing", r.RequestID)
		}
		parsed, err := envelope.Parse(record)
		if err != nil {
			t.Errorf("record %s does not read back as a rawcall: %v", r.RequestID, err)
			continue
		}
		if parsed.RequestID() != r.RequestID {
			t.Errorf("record at offset %d is %s, index says %s", r.Offset, parsed.RequestID(), r.RequestID)
		}
	}
}

func TestBuildMasksSecretsBeforePacking(t *testing.T) {
	rc := storedRawcall(t, "req-1", "session-a", "hash-p1",
		`{"model":"m","messages":[{"role":"user","content":"the key is `+fakeSecret+`"}]}`,
		`{"id":"req-1","type":"message"}`, buildTime)
	b, err := batch.Build("batch-1", buildTime, "test", []spool.Rawcall{rc}, batch.Run{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	stream := decompress(t, b.Records)
	if bytes.Contains(stream, []byte(fakeSecret)) {
		t.Fatal("secret survived into the packed records")
	}
	if !bytes.Contains(stream, []byte("REDACTED")) {
		t.Fatal("expected a REDACTED placeholder in the packed records")
	}
}

func TestBuildPreservesThinkingSignatures(t *testing.T) {
	rc := storedRawcall(t, "req-1", "session-a", "hash-p1",
		`{"model":"m","messages":[{"role":"user","content":"hello"}]}`,
		`{"id":"req-1","type":"message","content":[{"type":"thinking","thinking":"plan","signature":"`+fakeSignature+`"}]}`,
		buildTime)
	b, err := batch.Build("batch-1", buildTime, "test", []spool.Rawcall{rc}, batch.Run{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	stream := decompress(t, b.Records)
	if !bytes.Contains(stream, []byte(`"signature":"`+fakeSignature+`"`)) {
		t.Fatal("thinking signature was not preserved verbatim")
	}
}

func TestBuildEnvelopeCarriesIdentityIndexAndRunMetadata(t *testing.T) {
	rc := simpleRawcall(t, "req-1", "session-a", buildTime)
	run := batch.Run{
		RecordedToday:    7,
		SSEDegradedToday: 2,
		CapturesDropped:  1,
		SpoolUsageBytes:  4096,
		SpoolQuotaBytes:  2 << 30,
	}
	b, err := batch.Build("batch-42", buildTime, "1.2.3", []spool.Rawcall{rc}, run)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	env := parseEnvelope(t, b)
	if env.SchemaVersion != "1" || env.BatchID != "batch-42" || env.ClientVersion != "1.2.3" {
		t.Fatalf("envelope identity = %q/%q/%q", env.SchemaVersion, env.BatchID, env.ClientVersion)
	}
	if env.Compression != "zstd" {
		t.Fatalf("compression = %q, want zstd", env.Compression)
	}
	if env.CreatedAt != buildTime.Format(time.RFC3339Nano) {
		t.Fatalf("created_at = %q", env.CreatedAt)
	}
	r := env.Records[0]
	if r.ProjectIDHash != "hash-p1" || r.UpstreamOrigin != "official" || r.Endpoint != "/v1/messages" {
		t.Fatalf("index metadata = %+v", r)
	}
	if env.Run != run {
		t.Fatalf("run metadata = %+v, want %+v", env.Run, run)
	}
	if len(b.RequestIDs) != 1 || b.RequestIDs[0] != rc.RequestID {
		t.Fatalf("RequestIDs = %v", b.RequestIDs)
	}
}

func TestBuildPacksARecordWhoseEnvelopeCannotBeReadBack(t *testing.T) {
	broken := spool.Rawcall{
		RequestID: "req-broken",
		Timestamp: buildTime,
		Data:      []byte("not a rawcall at all"),
	}
	b, err := batch.Build("batch-1", buildTime, "test", []spool.Rawcall{broken}, batch.Run{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	env := parseEnvelope(t, b)
	if len(env.Records) != 1 || env.Records[0].RequestID != "req-broken" {
		t.Fatalf("index = %+v", env.Records)
	}
	stream := decompress(t, b.Records)
	r := env.Records[0]
	if got := string(stream[r.Offset : r.Offset+r.Size]); got != "not a rawcall at all" {
		t.Fatalf("packed record = %q", got)
	}
}

func TestBuildRefusesAnEmptyBatch(t *testing.T) {
	if _, err := batch.Build("batch-1", buildTime, "test", nil, batch.Run{}); err == nil {
		t.Fatal("expected an error for an empty batch")
	}
}

func TestBuildRefusesAMissingID(t *testing.T) {
	rc := simpleRawcall(t, "req-1", "session-a", buildTime)
	if _, err := batch.Build("", buildTime, "test", []spool.Rawcall{rc}, batch.Run{}); err == nil {
		t.Fatal("expected an error for a missing batch id")
	}
}

func TestBuildSessionAdjacencySurvivesALostIndex(t *testing.T) {
	sessioned := func(id, session string, at time.Time) spool.Rawcall {
		return storedRawcall(t, id, session, "hash-p1",
			`{"model":"m","metadata":{"user_id":"`+session+`"},"messages":[{"role":"user","content":"hello"}]}`,
			`{"id":"`+id+`","type":"message"}`, at)
	}
	indexed := []spool.Rawcall{
		sessioned("req-a1", "session-a", buildTime),
		sessioned("req-b1", "session-b", buildTime.Add(1*time.Second)),
		sessioned("req-a2", "session-a", buildTime.Add(2*time.Second)),
		sessioned("req-b2", "session-b", buildTime.Add(3*time.Second)),
	}
	unindexed := make([]spool.Rawcall, len(indexed))
	copy(unindexed, indexed)
	for i := range unindexed {
		unindexed[i].SessionKey = ""
	}

	order := func(rawcalls []spool.Rawcall) string {
		b, err := batch.Build("batch-1", buildTime, "test", rawcalls, batch.Run{})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		var ids []string
		for _, r := range parseEnvelope(t, b).Records {
			ids = append(ids, r.RequestID)
		}
		return strings.Join(ids, ",")
	}

	withIndex, withoutIndex := order(indexed), order(unindexed)
	if withIndex != "req-a1,req-a2,req-b1,req-b2" {
		t.Fatalf("indexed order = %s", withIndex)
	}
	if withoutIndex != withIndex {
		t.Errorf("order without the index = %s, want %s", withoutIndex, withIndex)
	}
}
