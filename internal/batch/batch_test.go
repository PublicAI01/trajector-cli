package batch_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/PublicAI01/trajector-cli/internal/batch"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/redact"
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

func rawcalls(rcs ...spool.Rawcall) batch.Contents {
	return batch.Contents{Rawcalls: rcs}
}

func storedSegment(t *testing.T, sessionID, file string, index int, at time.Time, lines string) spool.Record {
	t.Helper()
	seg := envelope.NewSegment(sessionID, file, index, transcriptCapture(at), lines)
	data, err := seg.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return spool.Record{ID: seg.RecordID, Kind: seg.RecordKind, SessionID: sessionID, ProjectIDHash: "hash-p1", Timestamp: at, Raw: data}
}

func storedSnapshot(t *testing.T, sessionID, file string, at time.Time, content string) spool.Record {
	t.Helper()
	snap, err := envelope.NewMetaSnapshot(sessionID, file, transcriptCapture(at), []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	data, err := snap.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return spool.Record{ID: snap.RecordID, Kind: snap.RecordKind, SessionID: sessionID, ProjectIDHash: "hash-p1", Timestamp: at, Raw: data}
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

func parseIndex(t *testing.T, b batch.Batch) batch.IndexV2 {
	t.Helper()
	ix, err := batch.ParseIndexV2(b.Envelope)
	if err != nil {
		t.Fatalf("batch envelope: %v", err)
	}
	return ix
}

func indexedIDs(t *testing.T, b batch.Batch) []string {
	t.Helper()
	var ids []string
	for _, item := range parseIndex(t, b).Records {
		ids = append(ids, item.RecordID)
	}
	return ids
}

func TestBuildLaysOutSameSessionRecordsAdjacently(t *testing.T) {
	in := rawcalls(
		simpleRawcall(t, "req-a1", "session-a", buildTime),
		simpleRawcall(t, "req-b1", "session-b", buildTime.Add(1*time.Second)),
		simpleRawcall(t, "req-a2", "session-a", buildTime.Add(2*time.Second)),
		simpleRawcall(t, "req-n1", "", buildTime.Add(3*time.Second)),
		simpleRawcall(t, "req-b2", "session-b", buildTime.Add(4*time.Second)),
	)
	b, _, err := batch.Build("batch-1", buildTime, "test", in, batch.Run{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []string{"req-a1", "req-a2", "req-b1", "req-b2", "req-n1"}
	if got := indexedIDs(t, b); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("record order = %v, want %v", got, want)
	}
}

func TestBuildRecordsRoundTripThroughTheIndex(t *testing.T) {
	in := rawcalls(
		simpleRawcall(t, "req-1", "session-a", buildTime),
		simpleRawcall(t, "req-2", "session-a", buildTime.Add(time.Second)),
		simpleRawcall(t, "req-3", "", buildTime.Add(2*time.Second)),
	)
	b, _, err := batch.Build("batch-1", buildTime, "test", in, batch.Run{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ix := parseIndex(t, b)
	stream := decompress(t, b.Records.Bytes())
	if int64(len(stream)) != ix.RecordsSize {
		t.Fatalf("decompressed %d bytes, envelope says %d", len(stream), ix.RecordsSize)
	}
	if len(ix.Records) != in.Len() {
		t.Fatalf("index has %d records, want %d", len(ix.Records), in.Len())
	}
	for _, r := range ix.Records {
		record := stream[r.Offset : r.Offset+r.Size]
		if !json.Valid(record) {
			t.Errorf("record %s is not valid JSON after packing", r.RecordID)
		}
		parsed, err := envelope.Parse(record)
		if err != nil {
			t.Errorf("record %s does not read back as a rawcall: %v", r.RecordID, err)
			continue
		}
		if parsed.RequestID() != r.RecordID {
			t.Errorf("record at offset %d is %s, index says %s", r.Offset, parsed.RequestID(), r.RecordID)
		}
	}
}

func TestBuildMasksSecretsBeforePacking(t *testing.T) {
	rc := storedRawcall(t, "req-1", "session-a", "hash-p1",
		`{"model":"m","messages":[{"role":"user","content":"the key is `+fakeSecret+`"}]}`,
		`{"id":"req-1","type":"message"}`, buildTime)
	b, _, err := batch.Build("batch-1", buildTime, "test", rawcalls(rc), batch.Run{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	stream := decompress(t, b.Records.Bytes())
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
	b, _, err := batch.Build("batch-1", buildTime, "test", rawcalls(rc), batch.Run{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	stream := decompress(t, b.Records.Bytes())
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
	b, _, err := batch.Build("batch-42", buildTime, "1.2.3", rawcalls(rc), run)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ix := parseIndex(t, b)
	if ix.SchemaVersion != "2" || ix.BatchID != "batch-42" || ix.ClientVersion != "1.2.3" {
		t.Fatalf("envelope identity = %q/%q/%q", ix.SchemaVersion, ix.BatchID, ix.ClientVersion)
	}
	if ix.Compression != "zstd" {
		t.Fatalf("compression = %q, want zstd", ix.Compression)
	}
	if ix.CreatedAt != buildTime.Format(time.RFC3339Nano) {
		t.Fatalf("created_at = %q", ix.CreatedAt)
	}
	r := ix.Records[0]
	if r.Source != "proxy" || r.ProjectIDHash != "hash-p1" || r.UpstreamOrigin != "official" || r.Endpoint != "/v1/messages" {
		t.Fatalf("index metadata = %+v", r)
	}
	if ix.Run != run {
		t.Fatalf("run metadata = %+v, want %+v", ix.Run, run)
	}
	if ids := b.Packed.IDs(); len(ids) != 1 || ids[0] != rc.RequestID {
		t.Fatalf("packed ids = %v", ids)
	}
}

func TestBuild_WritesSchemaVersionTwo(t *testing.T) {
	segment := storedSegment(t, "sess-1", "", 0, buildTime, `{"type":"user","message":{"role":"user","content":"hi"}}`+"\n")
	snapshot := storedSnapshot(t, "sess-1", "subagents/agent-x.meta.json", buildTime, `{"agentId":"x"}`)
	in := batch.Contents{
		Rawcalls:       []spool.Rawcall{simpleRawcall(t, "req-1", "session-a", buildTime)},
		SessionRecords: []spool.Record{segment, snapshot},
	}
	b, refused, err := batch.Build("batch-1", buildTime, "test", in, batch.Run{})
	if err != nil || len(refused) != 0 {
		t.Fatalf("Build: %v, refused %+v", err, refused)
	}
	if !strings.HasPrefix(string(b.Envelope), `{"schema_version":"2",`) {
		t.Fatalf("envelope = %s", b.Envelope)
	}
	ix := parseIndex(t, b)
	stream := decompress(t, b.Records.Bytes())
	if len(ix.Records) != 3 || int64(len(stream)) != ix.RecordsSize {
		t.Fatalf("index = %+v, stream %d bytes", ix.Records, len(stream))
	}
	seen := map[string]bool{}
	for i, item := range ix.Records {
		body := stream[item.Offset : item.Offset+item.Size]
		if seen[item.RecordID] {
			t.Errorf("record %d: id %s indexed twice", i, item.RecordID)
		}
		seen[item.RecordID] = true
		kind, err := envelope.KindOf(body)
		if err != nil || kind.Source != item.Source {
			t.Errorf("record %d: index says source %q, body says %+v (%v)", i, item.Source, kind, err)
		}
		switch kind {
		case envelope.KindRawcall:
			env, err := envelope.Parse(body)
			if err != nil || env.RequestID() != item.RecordID {
				t.Errorf("record %d: rawcall id %q, index %q (%v)", i, env.RequestID(), item.RecordID, err)
			}
			if item.Endpoint == "" || item.UpstreamOrigin == "" {
				t.Errorf("record %d: rawcall item lacks its route: %+v", i, item)
			}
		case envelope.KindSegment:
			seg, err := envelope.ParseSegment(body)
			if err != nil {
				t.Fatalf("record %d: %v", i, err)
			}
			if want := envelope.SegmentRecordID(seg.SessionID, seg.File, seg.SegmentIndex); seg.RecordID != want || item.RecordID != want {
				t.Errorf("record %d: segment id %q, index %q, recomputed %q", i, seg.RecordID, item.RecordID, want)
			}
			if item.Endpoint != "" || item.UpstreamOrigin != "" || item.Garbled {
				t.Errorf("record %d: segment item carries rawcall fields: %+v", i, item)
			}
		case envelope.KindMetaSnapshot:
			snap, err := envelope.ParseMetaSnapshot(body)
			if err != nil {
				t.Fatalf("record %d: %v", i, err)
			}
			want, err := envelope.MetaSnapshotRecordID(snap.SessionID, snap.File, snap.Content)
			if err != nil || snap.RecordID != want || item.RecordID != want {
				t.Errorf("record %d: snapshot id %q, index %q, recomputed %q (%v)", i, snap.RecordID, item.RecordID, want, err)
			}
		default:
			t.Errorf("record %d: unknown kind %+v", i, kind)
		}
	}
	if got := b.Packed.IDs(); strings.Join(got, ",") != "req-1,"+segment.ID+","+snapshot.ID {
		t.Errorf("packed ids = %v", got)
	}
}

func TestBuild_OrdersRawcallsFirstThenRecordsBySessionFileAndIndex(t *testing.T) {
	later := buildTime.Add(time.Minute)
	// Three segments of one file captured in the same instant: only the
	// segment index can order them, and their ids do not agree with it.
	seg0 := storedSegment(t, "sess-b", "", 0, buildTime, "{}\n")
	seg1 := storedSegment(t, "sess-b", "", 1, buildTime, "{}\n")
	seg2 := storedSegment(t, "sess-b", "", 2, buildTime, "{}\n")
	if seg0.ID < seg1.ID && seg1.ID < seg2.ID {
		t.Fatalf("segment ids %s/%s/%s sort in index order, so this case proves nothing; pick another session id", seg0.ID, seg1.ID, seg2.ID)
	}
	sub := storedSegment(t, "sess-b", "subagents/agent-x.jsonl", 0, buildTime, "{}\n")
	meta := storedSnapshot(t, "sess-b", "subagents/agent-x.meta.json", later, `{"agentId":"x"}`)
	other := storedSegment(t, "sess-a", "", 0, later, "{}\n")
	rc := simpleRawcall(t, "req-z", "session-z", later)

	in := batch.Contents{
		Rawcalls:       []spool.Rawcall{rc},
		SessionRecords: []spool.Record{meta, seg2, other, sub, seg0, seg1},
	}
	b, _, err := batch.Build("batch-1", buildTime, "test", in, batch.Run{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []string{"req-z", other.ID, seg0.ID, seg1.ID, seg2.ID, sub.ID, meta.ID}
	if got := indexedIDs(t, b); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("record order:\n got %v\nwant %v", got, want)
	}
	if got := b.Packed.IDs(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("packed order:\n got %v\nwant %v", got, want)
	}
}

func TestBuild_MasksSegmentsAndSnapshotsBeforePacking(t *testing.T) {
	segment := storedSegment(t, "sess-1", "", 0, buildTime,
		`{"type":"user","cwd":"/home/dev/proj","message":{"role":"user","content":"key `+fakeSecret+`"}}`+"\n")
	snapshot := storedSnapshot(t, "sess-1", "subagents/agent-x.meta.json", buildTime, `{"agentId":"x","note":"`+fakeSecret+`"}`)
	in := batch.Contents{SessionRecords: []spool.Record{segment, snapshot}}
	b, refused, err := batch.Build("batch-1", buildTime, "test", in, batch.Run{})
	if err != nil || len(refused) != 0 {
		t.Fatalf("Build: %v, refused %+v", err, refused)
	}
	stream := decompress(t, b.Records.Bytes())
	if bytes.Contains(stream, []byte(fakeSecret)) {
		t.Fatal("secret survived into the packed records")
	}
	if bytes.Contains(stream, []byte("/home/dev/proj")) {
		t.Fatal("the session's own directory survived into the packed segment")
	}
	ix := parseIndex(t, b)
	snapItem := ix.Records[1]
	if snapItem.RecordID == snapshot.ID {
		t.Errorf("snapshot indexed under its unmasked id %s; the id must name the masked content", snapshot.ID)
	}
	body := stream[snapItem.Offset : snapItem.Offset+snapItem.Size]
	snap, err := envelope.ParseMetaSnapshot(body)
	if err != nil {
		t.Fatal(err)
	}
	want, err := envelope.MetaSnapshotRecordID(snap.SessionID, snap.File, snap.Content)
	if err != nil || snap.RecordID != want || snapItem.RecordID != want {
		t.Errorf("snapshot id %q, index %q, recomputed from the packed content %q (%v)", snap.RecordID, snapItem.RecordID, want, err)
	}
	if ids := b.Packed.IDs(); len(ids) != 2 || ids[1] != snapshot.ID {
		t.Errorf("packed ids = %v, want the spool's own id %s for the snapshot", ids, snapshot.ID)
	}
}

func TestBuild_SegmentSignatureSurvivesPacking(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"` + fakeSignature + `"}]}}` + "\n"
	in := batch.Contents{SessionRecords: []spool.Record{storedSegment(t, "sess-1", "", 0, buildTime, line)}}
	b, _, err := batch.Build("batch-1", buildTime, "test", in, batch.Run{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	stream := decompress(t, b.Records.Bytes())
	seg, err := envelope.ParseSegment(stream)
	if err != nil {
		t.Fatal(err)
	}
	if seg.Lines != line {
		t.Fatalf("packed lines:\n got %q\nwant %q", seg.Lines, line)
	}
}

func TestBuild_RefusesRecordsItCannotReadOrMask(t *testing.T) {
	good := storedSegment(t, "sess-1", "", 0, buildTime, "{}\n")
	cut := storedSegment(t, "sess-1", "", 1, buildTime, `{"type":"user"}`)
	foreign := envelope.NewSegment("sess-1", "", 2, transcriptCapture(buildTime), "{}\n")
	foreign.RecordKind = "unknown_kind"
	foreignData, err := foreign.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		rec  spool.Record
		want error
	}{
		{"bytes that are not a record", spool.Record{ID: "rec-torn", Timestamp: buildTime, Raw: []byte("not a record")}, nil},
		{"a record of a kind no batch carries", spool.Record{ID: foreign.RecordID, Timestamp: buildTime, Raw: foreignData}, nil},
		{"a segment whose last line is cut", cut, redact.ErrIncompleteLine},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := batch.Contents{SessionRecords: []spool.Record{tc.rec, good}}
			b, refused, err := batch.Build("batch-1", buildTime, "test", in, batch.Run{})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if ids := b.Packed.IDs(); len(ids) != 1 || ids[0] != good.ID {
				t.Fatalf("packed = %v, want only the readable record", ids)
			}
			if len(refused) != 1 {
				t.Fatalf("refused = %+v, want the one record", refused)
			}
			r := refused[0]
			if r.ID() != tc.rec.ID || !bytes.Equal(r.Record.Raw, tc.rec.Raw) || r.Rawcall.RequestID != "" {
				t.Errorf("refusal = %+v, want the record carried whole in its own slot", r)
			}
			if r.Err == nil || (tc.want != nil && !errors.Is(r.Err, tc.want)) {
				t.Errorf("refusal error = %v, want %v", r.Err, tc.want)
			}
		})
	}
}

func TestBuildPacksTheRestAndReturnsEveryRefusalAtOnce(t *testing.T) {
	brokenOne := spool.Rawcall{RequestID: "req-broken-1", Timestamp: buildTime, Data: []byte("not a rawcall at all")}
	brokenTwo := spool.Rawcall{RequestID: "req-broken-2", Timestamp: buildTime.Add(time.Second), Data: []byte("{}")}
	good := simpleRawcall(t, "req-good", "session-a", buildTime)

	b, refused, err := batch.Build("batch-1", buildTime, "test", rawcalls(good, brokenOne, brokenTwo), batch.Run{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if ids := b.Packed.IDs(); len(ids) != 1 || ids[0] != good.RequestID {
		t.Fatalf("packed = %v, want only the readable record packed", ids)
	}
	if len(refused) != 2 {
		t.Fatalf("refused = %d record(s), want both broken ones listed in one pass", len(refused))
	}
	names := map[string]bool{}
	for _, r := range refused {
		names[r.ID()] = true
		if r.Err == nil {
			t.Errorf("refusal of %s carries no cause", r.ID())
		}
		if len(r.Rawcall.Data) == 0 || r.Record.ID != "" {
			t.Errorf("refusal of %s does not carry the rawcall whole in its own slot", r.ID())
		}
	}
	if !names["req-broken-1"] || !names["req-broken-2"] {
		t.Fatalf("refused records = %v, want req-broken-1 and req-broken-2", names)
	}
}

func TestBuildOfOnlyUnpackableRecordsReturnsNoBatchAndNoError(t *testing.T) {
	broken := spool.Rawcall{RequestID: "req-broken", Timestamp: buildTime, Data: []byte("not a rawcall at all")}
	b, refused, err := batch.Build("batch-1", buildTime, "test", rawcalls(broken), batch.Run{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if b.Packed.Len() != 0 || b.Envelope != nil {
		t.Fatalf("batch = %+v, want none when nothing packs", b)
	}
	if len(refused) != 1 || refused[0].ID() != "req-broken" {
		t.Fatalf("refused = %+v, want the one record listed", refused)
	}
}

func TestBuildRefusesAnEmptyBatch(t *testing.T) {
	if _, _, err := batch.Build("batch-1", buildTime, "test", batch.Contents{}, batch.Run{}); err == nil {
		t.Fatal("expected an error for an empty batch")
	}
}

func TestBuildRefusesAMissingID(t *testing.T) {
	rc := simpleRawcall(t, "req-1", "session-a", buildTime)
	if _, _, err := batch.Build("", buildTime, "test", rawcalls(rc), batch.Run{}); err == nil {
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

	order := func(rcs []spool.Rawcall) string {
		b, _, err := batch.Build("batch-1", buildTime, "test", rawcalls(rcs...), batch.Run{})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		return strings.Join(indexedIDs(t, b), ",")
	}

	withIndex, withoutIndex := order(indexed), order(unindexed)
	if withIndex != "req-a1,req-a2,req-b1,req-b2" {
		t.Fatalf("indexed order = %s", withIndex)
	}
	if withoutIndex != withIndex {
		t.Errorf("order without the index = %s, want %s", withoutIndex, withIndex)
	}
}
