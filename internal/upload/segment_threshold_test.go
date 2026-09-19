package upload_test

import (
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// storeTailSegment stores one record read from a session file while a
// session ran (tail_only injection), captured at the given time.
func (f *fixture) storeTailSegment(t *testing.T, sessionID string, at time.Time) {
	t.Helper()
	seg := envelope.NewSegment(sessionID, "", 0, envelope.Capture{
		ClientVersion: "test",
		Timestamp:     at.UTC().Format(time.RFC3339Nano),
		ProjectIDHash: "hash-p1",
		Injection:     envelope.InjectionTailOnly,
	}, `{"type":"user","message":{"role":"user","content":"hi"}}`+"\n")
	if err := f.spool.WriteSegment(seg); err != nil {
		t.Fatal(err)
	}
}

func TestFreshSessionFileRecordsStayBelowTheirOwnThreshold(t *testing.T) {
	f := newFixture(t)
	f.storeTailSegment(t, "sess-1", f.now)

	res, err := f.uploader.Flush(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.BelowThreshold {
		t.Fatalf("outcome = %q, want %q", res.Outcome, upload.BelowThreshold)
	}
	if got := len(f.server.Requests()); got != 0 {
		t.Errorf("service saw %d requests, want 0", got)
	}
}

func TestSessionFileRecordsLeaveAfterFiveMinutesNotADay(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeTailSegment(t, "sess-1", f.now.Add(-6*time.Minute))
	f.storeRawcall(t, "req-1", f.now)

	res, err := f.uploader.Flush(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.Uploaded || res.Records != 2 {
		t.Fatalf("result = %+v, want both records uploaded once the segment is due", res)
	}
}

func TestSessionFileRecordThresholdsFollowTheHandshake(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, map[string]any{"segment_flush_age_seconds": 3600}))
	f.storeTailSegment(t, "sess-1", f.now.Add(-6*time.Minute))
	if _, err := f.uploader.Flush(true); err != nil {
		t.Fatal(err)
	}
	if got := upload.LoadHandshake(f.dir).SegmentFlushAgeSeconds; got != 3600 {
		t.Fatalf("stored handshake age = %d, want 3600", got)
	}

	f.storeTailSegment(t, "sess-2", f.now.Add(-6*time.Minute))
	res, err := f.uploader.Flush(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.BelowThreshold {
		t.Errorf("outcome = %q, want %q under the handshake's hour", res.Outcome, upload.BelowThreshold)
	}
}

func TestFlushRecordsSendsFreshSessionFileRecordsAtOnce(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeTailSegment(t, "sess-1", f.now)

	res, err := f.uploader.FlushRecords()
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.Uploaded || res.Records != 1 {
		t.Fatalf("result = %+v, want the fresh segment uploaded", res)
	}
}

func TestFlushRecordsLeavesASpoolOfFreshCallsAlone(t *testing.T) {
	f := newFixture(t)
	f.storeRawcall(t, "req-1", f.now)

	res, err := f.uploader.FlushRecords()
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.BelowThreshold {
		t.Errorf("outcome = %q, want %q: no session file record asked for a flush", res.Outcome, upload.BelowThreshold)
	}
}
