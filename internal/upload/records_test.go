package upload_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/PublicAI01/trajector-cli/internal/batch"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

const (
	fakeSecret    = "sk-test-fake-xK9mZ2vL8nQ5rT1wY4bC7dF0gH3jE6pA"
	fakeSignature = "EqQBCkgIBBgCIkDunT5RmZFPqBWEcTbEK4DZWWL9zGnDx0M0vGRnHkV6wZQx"
	sessionX      = "0a1b2c3d-1111-4aaa-8aaa-000000000001"
	sessionY      = "0a1b2c3d-2222-4bbb-8bbb-000000000002"
)

func recordCapture(at time.Time, projectIDHash string) envelope.TranscriptCapture {
	return envelope.TranscriptCapture{
		ClientVersion: "test",
		Timestamp:     at.UTC().Format(time.RFC3339Nano),
		ProjectIDHash: projectIDHash,
		Injection:     envelope.InjectionProxy,
	}
}

func (f *fixture) storeSegment(t *testing.T, sessionID, file string, index int, at time.Time, lines string) envelope.Segment {
	t.Helper()
	return f.storeSegmentFor(t, sessionID, "hash-p1", file, index, at, lines)
}

func (f *fixture) storeSegmentFor(t *testing.T, sessionID, projectIDHash, file string, index int, at time.Time, lines string) envelope.Segment {
	t.Helper()
	seg := envelope.NewSegment(sessionID, file, index, recordCapture(at, projectIDHash), lines)
	if err := f.spool.WriteSegment(seg); err != nil {
		t.Fatal(err)
	}
	return seg
}

func (f *fixture) storeSnapshot(t *testing.T, sessionID, file string, at time.Time, content string) envelope.MetaSnapshot {
	t.Helper()
	snap, err := envelope.NewMetaSnapshot(sessionID, file, recordCapture(at, "hash-p1"), []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.spool.WriteMetaSnapshot(snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

// storeRawcallOf stores a rawcall whose request names sessionKey, so
// the batch groups it with that session.
func (f *fixture) storeRawcallOf(t *testing.T, id, sessionKey string, at time.Time) {
	t.Helper()
	env, err := envelope.Record(envelope.Observation{
		Provider: "anthropic", Endpoint: "/v1/messages", HTTPStatus: 200, ClientVersion: "test",
		ProjectIDHash: "hash-p1", At: at,
		Upstream: "https://api.anthropic.com", OfficialUpstream: "https://api.anthropic.com",
		Request:          []byte(`{"model":"m","metadata":{"user_id":"` + sessionKey + `"},"messages":[{"role":"user","content":"hello"}]}`),
		RequestComplete:  true,
		Response:         []byte(`{"id":"` + id + `","type":"message"}`),
		ResponseComplete: true,
		ContentType:      "application/json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.spool.Write(env); err != nil {
		t.Fatal(err)
	}
}

// storeUnreadableRecord plants bytes in the record slot that parse as
// no record, the way a write torn by a crash leaves them.
func (f *fixture) storeUnreadableRecord(t *testing.T, id string, raw []byte) {
	t.Helper()
	day := filepath.Join(f.spoolDir, "records", time.Now().UTC().Format("20060102"))
	if err := os.MkdirAll(day, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(day, id+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func spooledRecordIDs(t *testing.T, sp *spool.Spool) []string {
	t.Helper()
	var ids []string
	if err := sp.EachRecord(func(r spool.Record) error { ids = append(ids, r.ID); return nil }); err != nil {
		t.Fatal(err)
	}
	return ids
}

func spooledRawcallIDs(t *testing.T, sp *spool.Spool) []string {
	t.Helper()
	var ids []string
	if err := sp.Each(func(r spool.Rawcall) error { ids = append(ids, r.RequestID); return nil }); err != nil {
		t.Fatal(err)
	}
	return ids
}

// uploadedIndex reads the index one upload carried.
func uploadedIndex(t *testing.T, r fakeplatform.Request) batch.IndexV2 {
	t.Helper()
	ix, err := fakeplatform.UploadedIndex(r)
	if err != nil {
		t.Fatalf("reading upload request: %v", err)
	}
	return ix
}

// uploadedStream decompresses the record stream one upload carried.
func uploadedStream(t *testing.T, r fakeplatform.Request) []byte {
	t.Helper()
	parts, err := fakeplatform.Parts(r)
	if err != nil {
		t.Fatalf("reading upload request: %v", err)
	}
	zr, err := zstd.NewReader(bytes.NewReader(parts["records"]))
	if err != nil {
		t.Fatalf("opening the record stream: %v", err)
	}
	defer zr.Close()
	stream, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompressing the record stream: %v", err)
	}
	return stream
}

func indexedRecordIDs(ix batch.IndexV2) []string {
	var ids []string
	for _, item := range ix.Records {
		ids = append(ids, item.RecordID)
	}
	return ids
}

func TestFlush_MixesBothRecordKindsInOneBatch(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", f.now)
	seg := f.storeSegment(t, sessionX, "", 0, f.now, `{"type":"user","message":{"role":"user","content":"hi"}}`+"\n")
	snap := f.storeSnapshot(t, sessionX, "subagents/agent-a.meta.json", f.now, `{"agentId":"a"}`)

	res, err := f.uploader.Flush(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.Uploaded || res.Batches != 1 || res.Records != 3 {
		t.Fatalf("result = %+v, want one batch of three records", res)
	}
	reqs := f.server.Requests()
	if len(reqs) != 1 {
		t.Fatalf("service saw %d requests, want 1", len(reqs))
	}
	ix := uploadedIndex(t, reqs[0])
	want := []string{"req-1", seg.RecordID, snap.RecordID}
	if got := indexedRecordIDs(ix); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("index = %v, want %v", got, want)
	}
	routed, err := fakeplatform.RecordIDsBySource(mustPart(t, reqs[0], "batch"))
	if err != nil {
		t.Fatal(err)
	}
	if len(routed["proxy"]) != 1 || len(routed["transcript"]) != 2 {
		t.Errorf("routing by source = %v", routed)
	}
	stream := uploadedStream(t, reqs[0])
	if int64(len(stream)) != ix.RecordsSize {
		t.Fatalf("stream is %d bytes, index says %d", len(stream), ix.RecordsSize)
	}
	for _, item := range ix.Records {
		kind, err := envelope.KindOf(stream[item.Offset : item.Offset+item.Size])
		if err != nil || kind.Source != item.Source {
			t.Errorf("record %s: body declares %+v, index says %q (%v)", item.RecordID, kind, item.Source, err)
		}
	}
}

func mustPart(t *testing.T, r fakeplatform.Request, name string) []byte {
	t.Helper()
	parts, err := fakeplatform.Parts(r)
	if err != nil {
		t.Fatal(err)
	}
	return parts[name]
}

func TestFlush_SegmentSignatureBytesReachTheService(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	line := `{"type":"assistant","cwd":"/home/dev/proj","message":{"id":"msg_1","role":"assistant","content":[{"type":"thinking","thinking":"","signature":"` + fakeSignature + `"},{"type":"text","text":"hi"}]}}` + "\n"
	seg := f.storeSegment(t, sessionX, "", 0, f.now, line)
	var stored envelope.Segment
	if err := f.spool.EachRecord(func(r spool.Record) error {
		var err error
		stored, err = envelope.ParseSegment(r.Raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := f.uploader.Flush(true); err != nil {
		t.Fatal(err)
	}
	reqs := f.server.Requests()
	if len(reqs) != 1 {
		t.Fatalf("service saw %d requests, want 1", len(reqs))
	}
	got, err := envelope.ParseSegment(uploadedStream(t, reqs[0]))
	if err != nil {
		t.Fatalf("the service received no readable segment: %v", err)
	}
	if got.RecordID != seg.RecordID || got.SegmentIndex != seg.SegmentIndex || got.SessionID != seg.SessionID {
		t.Errorf("segment identity = %s/%s/%d, want %s/%s/%d", got.RecordID, got.SessionID, got.SegmentIndex, seg.RecordID, seg.SessionID, seg.SegmentIndex)
	}
	sig := `"signature":"` + fakeSignature + `"`
	if !strings.Contains(stored.Lines, sig) || !strings.Contains(got.Lines, sig) {
		t.Errorf("signature bytes did not reach the service intact:\nspool   %s\nservice %s", stored.Lines, got.Lines)
	}
	if strings.Contains(got.Lines, "/home/dev/proj") {
		t.Errorf("the session's own directory reached the service: %s", got.Lines)
	}
	if !strings.HasSuffix(got.Lines, "\n") {
		t.Errorf("lines lost their newline: %q", got.Lines)
	}
}

func TestFlush_AckDeletesBothKinds(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", f.now)
	f.storeSegment(t, sessionX, "", 0, f.now, "{}\n")
	f.storeSnapshot(t, sessionX, "subagents/agent-a.meta.json", f.now, `{"agentId":"a"}`)

	if _, err := f.uploader.Flush(true); err != nil {
		t.Fatal(err)
	}
	if ids := spooledRawcallIDs(t, f.spool); len(ids) != 0 {
		t.Errorf("rawcalls still spooled after the ack: %v", ids)
	}
	if ids := spooledRecordIDs(t, f.spool); len(ids) != 0 {
		t.Errorf("records still spooled after the ack: %v", ids)
	}
	if usage := f.spool.Usage(); usage != 0 {
		t.Errorf("spool usage = %d after the ack, want 0", usage)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "pending.json")); !os.IsNotExist(err) {
		t.Error("the acknowledged batch is still pending")
	}
}

func TestFlush_FailureKeepsBothKinds(t *testing.T) {
	cases := []struct {
		name     string
		response fakeplatform.Response
		want     upload.Disposition
	}{
		{"service error", rejectStub(503, "down"), upload.RetrySameID},
		{"unauthorized", rejectStub(401, "bad token"), upload.RetrySameID},
		{"rate limited", rejectStub(429, "slow down"), upload.RetrySameID},
		{"upgrade required", fakeplatform.Refuses426("9.0.0", "please upgrade"), upload.PauseUploads},
		{"authorization required", fakeplatform.Refuses451("https://example.com/authorize", ""), upload.PauseUploadsAuthorize},
		{"ack for another batch", fakeplatform.JSON(200, map[string]any{"batch_id": "b-someone-else"}), upload.RetrySameID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.server.Stub("POST", "/v1/batches", tc.response)
			f.storeRawcall(t, "req-1", f.now)
			seg := f.storeSegment(t, sessionX, "", 0, f.now, "{}\n")
			snap := f.storeSnapshot(t, sessionX, "subagents/agent-a.meta.json", f.now, `{"agentId":"a"}`)

			res, err := f.uploader.Flush(true)
			if err == nil {
				t.Fatal("a refused upload reported no error")
			}
			if res.Disposition != tc.want {
				t.Errorf("disposition = %q, want %q", res.Disposition, tc.want)
			}
			if ids := spooledRawcallIDs(t, f.spool); strings.Join(ids, ",") != "req-1" {
				t.Errorf("rawcalls spooled = %v, want req-1 kept", ids)
			}
			if ids := spooledRecordIDs(t, f.spool); len(ids) != 2 || ids[0] != min(seg.RecordID, snap.RecordID) {
				t.Errorf("records spooled = %v, want both kept", ids)
			}
			if n := rejectedRecords(t, f.rejected); n != 0 {
				t.Errorf("rejected store holds %d records, want none quarantined", n)
			}
		})
	}
}

func TestFlush_RejectedMixedBatchIsQuarantinedWholeAndRequeuesToBothSlots(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", rejectStub(400, "poison"))
	f.storeRawcall(t, "req-1", f.now)
	seg := f.storeSegment(t, sessionX, "", 0, f.now, "{}\n")
	snap := f.storeSnapshot(t, sessionX, "subagents/agent-a.meta.json", f.now, `{"agentId":"a"}`)

	res, _ := f.uploader.Flush(true)
	if res.Outcome != upload.Rejected {
		t.Fatalf("result = %+v, want the batch rejected", res)
	}
	if f.spool.Usage() != 0 {
		t.Errorf("spool usage = %d, want both slots emptied into the quarantine", f.spool.Usage())
	}
	batches, err := upload.ListRejected(f.rejected)
	if err != nil || len(batches) != 1 || batches[0].Records != 3 {
		t.Fatalf("rejected store = %+v, %v; want one batch of three records", batches, err)
	}
	for _, name := range []string{"req-1.json", seg.RecordID + ".json", snap.RecordID + ".json"} {
		if _, err := os.Stat(filepath.Join(f.rejected, batches[0].BatchID, name)); err != nil {
			t.Errorf("quarantined record missing: %v", err)
		}
	}

	rej, moved, err := upload.Requeue(f.rejected, f.spool, batches[0].BatchID)
	if err != nil || moved != 3 || rej.Cause != upload.CauseRefused {
		t.Fatalf("requeue = %+v, %d, %v; want every record moved back", rej, moved, err)
	}
	if ids := spooledRawcallIDs(t, f.spool); strings.Join(ids, ",") != "req-1" {
		t.Errorf("rawcalls after requeue = %v, want req-1 back in its slot", ids)
	}
	if ids := spooledRecordIDs(t, f.spool); len(ids) != 2 {
		t.Errorf("records after requeue = %v, want the segment and the snapshot back in theirs", ids)
	}
	if n := rejectedRecords(t, f.rejected); n != 0 {
		t.Errorf("rejected store still holds %d records", n)
	}
}

func TestFlush_OrdersAdjacentBySourceThenSession(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcallOf(t, "req-b1", "sess-b", f.now)
	f.storeRawcallOf(t, "req-a1", "sess-a", f.now.Add(time.Second))
	f.storeRawcallOf(t, "req-b2", "sess-b", f.now.Add(2*time.Second))
	y0 := f.storeSegment(t, sessionY, "", 0, f.now, "{}\n")
	x0 := f.storeSegment(t, sessionX, "", 0, f.now.Add(time.Second), "{}\n")
	y1 := f.storeSegment(t, sessionY, "", 1, f.now.Add(time.Second), "{}\n")
	xMeta := f.storeSnapshot(t, sessionX, "subagents/agent-a.meta.json", f.now, `{"agentId":"a"}`)

	if _, err := f.uploader.Flush(true); err != nil {
		t.Fatal(err)
	}
	reqs := f.server.Requests()
	if len(reqs) != 1 {
		t.Fatalf("service saw %d requests, want 1", len(reqs))
	}
	want := []string{"req-a1", "req-b1", "req-b2", x0.RecordID, xMeta.RecordID, y0.RecordID, y1.RecordID}
	if got := indexedRecordIDs(uploadedIndex(t, reqs[0])); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("stream order:\n got %v\nwant %v", got, want)
	}
}

func TestFlush_IncompleteSegmentIsSetAside(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, f *fixture) string
	}{
		{"a segment whose last line is cut", func(t *testing.T, f *fixture) string {
			return f.storeSegment(t, sessionX, "", 1, f.now, `{"type":"user"}`).RecordID
		}},
		{"bytes that are not a record", func(t *testing.T, f *fixture) string {
			f.storeUnreadableRecord(t, "rec-torn", []byte(`{"schema_version":"2","source":"transcript","record_kind":"segment","record_id":"rec-torn"`))
			return "rec-torn"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
			good := f.storeSegment(t, sessionX, "", 0, f.now, "{}\n")
			bad := tc.plant(t, f)

			res, err := f.uploader.Flush(true)
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != upload.Uploaded || res.Records != 1 || len(res.SetAside) != 1 || res.SetAside[0].Cause != upload.CauseUnreadable {
				t.Fatalf("result = %+v, want the readable segment uploaded and the other set aside", res)
			}
			if !strings.Contains(res.SetAside[0].Details, bad) {
				t.Errorf("details = %q, want the set-aside record named", res.SetAside[0].Details)
			}
			reqs := f.server.Requests()
			if len(reqs) != 1 {
				t.Fatalf("service saw %d requests, want 1", len(reqs))
			}
			if got := indexedRecordIDs(uploadedIndex(t, reqs[0])); strings.Join(got, ",") != good.RecordID {
				t.Errorf("uploaded = %v, want only %s", got, good.RecordID)
			}
			if ids := spooledRecordIDs(t, f.spool); len(ids) != 0 {
				t.Errorf("records still spooled: %v", ids)
			}
			if _, err := os.Stat(filepath.Join(f.rejected, res.SetAside[0].BatchID, bad+".json")); err != nil {
				t.Errorf("the set-aside record is not in the quarantine: %v", err)
			}
			if _, _, err := upload.Requeue(f.rejected, f.spool, res.SetAside[0].BatchID); err == nil {
				t.Error("a record that cannot be masked was requeued")
			}
		})
	}
}

func TestFlush_SnapshotIDFollowsRedactedContent(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	snap := f.storeSnapshot(t, sessionX, "subagents/agent-a.meta.json", f.now, `{"agentId":"a","note":"`+fakeSecret+`"}`)

	res, err := f.uploader.Flush(true)
	if err != nil || res.Records != 1 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	reqs := f.server.Requests()
	if len(reqs) != 1 {
		t.Fatalf("service saw %d requests, want 1", len(reqs))
	}
	ix := uploadedIndex(t, reqs[0])
	stream := uploadedStream(t, reqs[0])
	if bytes.Contains(stream, []byte(fakeSecret)) {
		t.Fatal("the secret reached the service")
	}
	got, err := envelope.ParseMetaSnapshot(stream)
	if err != nil {
		t.Fatal(err)
	}
	want, err := envelope.MetaSnapshotRecordID(got.SessionID, got.File, got.Content)
	if err != nil {
		t.Fatal(err)
	}
	if got.RecordID != want || ix.Records[0].RecordID != want {
		t.Errorf("snapshot id %q, index %q; the masked content names %q", got.RecordID, ix.Records[0].RecordID, want)
	}
	if want == snap.RecordID {
		t.Errorf("the id did not change although the content did")
	}
	if ids := spooledRecordIDs(t, f.spool); len(ids) != 0 {
		t.Errorf("the acknowledged snapshot is still spooled under its stored id: %v", ids)
	}
}

func TestFlush_NeverSendsRecordsOfAProjectThatWithdrewConsent(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	kept := f.storeSegmentFor(t, sessionX, "hash-granted", "", 0, f.now, "{}\n")
	gone := f.storeSegmentFor(t, sessionY, "hash-withdrawn", "", 0, f.now, "{}\n")
	f.withdrawn["hash-withdrawn"] = true

	res, err := f.uploader.Flush(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.Uploaded || res.Records != 1 {
		t.Fatalf("result = %+v, want only the granted project's segment acknowledged", res)
	}
	for _, r := range f.server.Requests() {
		if strings.Contains(string(mustPart(t, r, "batch")), gone.RecordID) {
			t.Fatalf("a batch carried a record whose project had withdrawn consent")
		}
		if !strings.Contains(string(mustPart(t, r, "batch")), kept.RecordID) {
			t.Fatalf("the granted project's segment was not sent")
		}
	}
	if usage := f.spool.Usage(); usage != 0 {
		t.Errorf("spool holds %d bytes; the withdrawn record was neither sent nor deleted", usage)
	}
}

func TestFlush_PendingBatchResendsBothKindsUnderTheSameID(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", rejectStub(503, "down"))
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", f.now)
	seg := f.storeSegment(t, sessionX, "", 0, f.now, "{}\n")

	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("first flush against a failing service did not error")
	}
	res, err := f.newUploader(t).Flush(true)
	if err != nil || res.Outcome != upload.Uploaded || res.Records != 2 {
		t.Fatalf("resend = %+v, %v; want both records acknowledged", res, err)
	}
	reqs := f.server.Requests()
	if len(reqs) != 2 {
		t.Fatalf("service saw %d requests, want 2", len(reqs))
	}
	first, second := uploadedIndex(t, reqs[0]), uploadedIndex(t, reqs[1])
	if first.BatchID == "" || first.BatchID != second.BatchID {
		t.Errorf("resend used batch id %q, first attempt %q; they must match", second.BatchID, first.BatchID)
	}
	want := "req-1," + seg.RecordID
	if got := strings.Join(indexedRecordIDs(second), ","); got != want {
		t.Errorf("resend carried %s, want %s", got, want)
	}
}

func TestFlush_APendingFileFromAnEarlierBuildKeepsItsID(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", f.now)
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	earlier := []byte(`{"batch_id":"b-from-an-earlier-build","request_ids":["req-1"]}`)
	if err := os.WriteFile(filepath.Join(f.dir, "pending.json"), earlier, 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := f.uploader.Flush(true)
	if err != nil || res.Records != 1 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	reqs := f.server.Requests()
	if len(reqs) != 1 {
		t.Fatalf("service saw %d requests, want 1", len(reqs))
	}
	if got := uploadedIndex(t, reqs[0]).BatchID; got != "b-from-an-earlier-build" {
		t.Errorf("batch id = %q, want the id the earlier build pinned", got)
	}
}

func TestFlush_AgedRecordsInTheSecondSlotTriggerAnUnforcedFlush(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeSegment(t, sessionX, "", 0, f.now.Add(-25*time.Hour), "{}\n")

	res, err := f.uploader.Flush(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.Uploaded || res.Records != 1 {
		t.Fatalf("result = %+v, want the aged segment uploaded", res)
	}
}

func TestRequeue_ReturnsRecordsToTheirSlot(t *testing.T) {
	rejectedDir, sp := t.TempDir(), openSpool(t)
	at := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	seg := envelope.NewSegment(sessionX, "", 0, recordCapture(at, "hash-p1"), "{}\n")
	segBytes, err := seg.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	snap, err := envelope.NewMetaSnapshot(sessionX, "subagents/agent-a.meta.json", recordCapture(at, "hash-p1"), []byte(`{"agentId":"a"}`))
	if err != nil {
		t.Fatal(err)
	}
	snapBytes, err := snap.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	seedBatch(t, rejectedDir, "b-mixed", map[string][]byte{
		"req-1":       rawcallBytes(t, "req-1"),
		seg.RecordID:  segBytes,
		snap.RecordID: snapBytes,
	})

	_, moved, err := upload.Requeue(rejectedDir, sp, "b-mixed")
	if err != nil || moved != 3 {
		t.Fatalf("requeue = %d, %v; want all three moved", moved, err)
	}
	if ids := spooledRawcallIDs(t, sp); strings.Join(ids, ",") != "req-1" {
		t.Errorf("rawcall slot = %v, want req-1", ids)
	}
	var kinds []string
	if err := sp.EachRecord(func(r spool.Record) error {
		kinds = append(kinds, r.ID+"="+r.Kind)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{seg.RecordID + "=segment": true, snap.RecordID + "=meta_snapshot": true}
	if len(kinds) != 2 || !want[kinds[0]] || !want[kinds[1]] {
		t.Errorf("record slot = %v, want the segment and the snapshot attributed by kind", kinds)
	}
	if _, err := os.Stat(filepath.Join(rejectedDir, "b-mixed")); !os.IsNotExist(err) {
		t.Error("batch directory still present after a full requeue")
	}
}

func TestRequeue_KeepsARecordOfAnUnknownKindQuarantined(t *testing.T) {
	rejectedDir, sp := t.TempDir(), openSpool(t)
	seedBatch(t, rejectedDir, "b-mixed", map[string][]byte{
		"req-1":   rawcallBytes(t, "req-1"),
		"rec-odd": []byte(`{"schema_version":"2","source":"transcript","record_kind":"unknown_kind","record_id":"rec-odd"}`),
	})

	_, moved, err := upload.Requeue(rejectedDir, sp, "b-mixed")
	if err == nil || !strings.Contains(err.Error(), "rec-odd") {
		t.Fatalf("err = %v, want the stuck record named", err)
	}
	if moved != 1 {
		t.Errorf("moved = %d, want the readable rawcall moved regardless", moved)
	}
	if _, err := os.Stat(filepath.Join(rejectedDir, "b-mixed", "rec-odd.json")); err != nil {
		t.Errorf("the unreadable record must stay quarantined: %v", err)
	}
	if ids := spooledRecordIDs(t, sp); len(ids) != 0 {
		t.Errorf("record slot = %v, want nothing of an unknown kind stored", ids)
	}
}

func TestFlush_UnmaskableRecordTriggersTheCallback(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, f *fixture)
		want  int
	}{
		{"a segment whose last line is cut cannot be masked", func(t *testing.T, f *fixture) {
			f.storeSegment(t, sessionX, "", 1, f.now, `{"type":"user"}`)
		}, 1},
		{"two such segments in one flush report once", func(t *testing.T, f *fixture) {
			f.storeSegment(t, sessionX, "", 1, f.now, `{"type":"user"}`)
			f.storeSegment(t, sessionY, "", 0, f.now, `{"type":"user"}`)
		}, 1},
		{"bytes that are not a record are unreadable, not unmaskable", func(t *testing.T, f *fixture) {
			f.storeUnreadableRecord(t, "rec-torn", []byte(`{"schema_version":"2","source":"transcript","record_kind":"segment","record_id":"rec-torn"`))
		}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
			f.storeSegment(t, sessionX, "", 0, f.now, "{}\n")
			tc.plant(t, f)

			res, err := f.uploader.Flush(true)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.SetAside) != 1 {
				t.Fatalf("result = %+v, want one set-aside", res)
			}
			if f.unmaskable != tc.want {
				t.Errorf("unmaskable reported %d time(s), want %d", f.unmaskable, tc.want)
			}
		})
	}
}
