package upload_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/batch"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

type fixture struct {
	spool    *spool.Spool
	server   *fakeplatform.Server
	uploader *upload.Uploader
	dir      string
	token    string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		server: fakeplatform.New(t),
		dir:    filepath.Join(t.TempDir(), "upload"),
		token:  "dev-tok-fake",
	}
	sp, err := spool.Create(filepath.Join(t.TempDir(), "rawcalls"), 0)
	if err != nil {
		t.Fatal(err)
	}
	f.spool = sp
	u, err := upload.New(upload.Deps{
		Spool:       sp,
		Service:     platform.New(f.server.URL(), "test"),
		DeviceToken: func() (string, error) { return f.token, nil },
		Version:     "1.0.0",
		Dir:         f.dir,
		Run: func() batch.Run {
			return batch.Run{RecordedToday: 5, SpoolUsageBytes: sp.Usage(), SpoolQuotaBytes: sp.Quota()}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.uploader = u
	return f
}

func (f *fixture) storeRawcall(t *testing.T, id string, at time.Time) {
	t.Helper()
	env, err := envelope.Record(envelope.Observation{
		Provider:          "anthropic",
		Endpoint:          "/v1/messages",
		HTTPStatus:        200,
		ClientVersion:     "test",
		ProjectIDHash:     "hash-p1",
		At:                at,
		Upstream:          "https://api.anthropic.com",
		OfficialUpstream:  "https://api.anthropic.com",
		Request:           []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`),
		RequestComplete:   true,
		Response:          []byte(`{"id":"` + id + `","type":"message"}`),
		ResponseComplete:  true,
		ContentType:       "application/json",
		UpstreamRequestID: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := spool.Entry{RequestID: env.RequestID(), SessionKey: "sess-1", Timestamp: at}
	if err := f.spool.Write(entry, env.Bytes()); err != nil {
		t.Fatal(err)
	}
}

// echoAck acknowledges whatever batch id the request carries, plus any
// handshake fields.
func echoAck(t *testing.T, handshake map[string]any) func(fakeplatform.Request) fakeplatform.Response {
	return func(r fakeplatform.Request) fakeplatform.Response {
		t.Helper()
		body := map[string]any{"batch_id": uploadedBatchID(t, r)}
		for k, v := range handshake {
			body[k] = v
		}
		return fakeplatform.JSON(200, body)
	}
}

func uploadedBatchID(t *testing.T, r fakeplatform.Request) string {
	t.Helper()
	parts, err := fakeplatform.Parts(r)
	if err != nil {
		t.Fatalf("reading upload request: %v", err)
	}
	var env struct {
		BatchID string `json:"batch_id"`
	}
	if err := json.Unmarshal(parts["batch"], &env); err != nil {
		t.Fatalf("reading batch envelope: %v", err)
	}
	return env.BatchID
}

func TestUnforcedFlushBelowThresholdsLeavesTheSpoolAlone(t *testing.T) {
	f := newFixture(t)
	f.storeRawcall(t, "req-1", time.Now().UTC())

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
	if f.spool.Usage() == 0 {
		t.Error("spool was drained below threshold")
	}
}

func TestForcedFlushUploadsAndDeletesAcknowledgedRecords(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", time.Now().UTC())
	f.storeRawcall(t, "req-2", time.Now().UTC())

	res, err := f.uploader.Flush(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.Uploaded || res.Batches != 1 || res.Records != 2 {
		t.Fatalf("result = %+v", res)
	}
	if usage := f.spool.Usage(); usage != 0 {
		t.Errorf("spool usage after acknowledgement = %d, want 0", usage)
	}
	reqs := f.server.Requests()
	if len(reqs) != 1 {
		t.Fatalf("service saw %d requests", len(reqs))
	}
	if got := reqs[0].Header.Get("Authorization"); got != "Bearer dev-tok-fake" {
		t.Errorf("authorization = %q", got)
	}
	st := upload.LoadState(f.dir)
	if st.LastUpload == nil || st.LastUpload.Records != 2 {
		t.Errorf("state after upload = %+v", st)
	}
	if st.LastError != "" {
		t.Errorf("state carries an error after success: %q", st.LastError)
	}
}

func TestFlushWithoutADeviceTokenPauses(t *testing.T) {
	f := newFixture(t)
	f.token = ""
	f.storeRawcall(t, "req-1", time.Now().UTC())

	res, err := f.uploader.Flush(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.Paused {
		t.Fatalf("outcome = %q, want %q", res.Outcome, upload.Paused)
	}
	if got := len(f.server.Requests()); got != 0 {
		t.Errorf("service saw %d requests while signed out", got)
	}
	if f.spool.Usage() == 0 {
		t.Error("spool was drained while signed out")
	}
}

func TestEmptySpoolFlushesToNothing(t *testing.T) {
	f := newFixture(t)
	res, err := f.uploader.Flush(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.Empty || len(f.server.Requests()) != 0 {
		t.Fatalf("result = %+v with %d requests", res, len(f.server.Requests()))
	}
}

func TestAgedRecordsTriggerAnUnforcedFlush(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", time.Now().UTC().Add(-25*time.Hour))

	res, err := f.uploader.Flush(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.Uploaded {
		t.Fatalf("outcome = %q, want %q", res.Outcome, upload.Uploaded)
	}
}

func TestFailedUploadKeepsRecordsAndRetriesUnderTheSameBatchID(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", fakeplatform.JSON(503, map[string]any{"error": "down"}))
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", time.Now().UTC())
	usageBefore := f.spool.Usage()

	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("first flush against a failing service did not error")
	}
	if f.spool.Usage() != usageBefore {
		t.Fatal("failed upload changed the spool")
	}
	if st := upload.LoadState(f.dir); st.LastError == "" {
		t.Error("failed attempt left no error in state")
	}

	res, err := f.uploader.Flush(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.Uploaded {
		t.Fatalf("second flush = %+v", res)
	}
	reqs := f.server.Requests()
	if len(reqs) != 2 {
		t.Fatalf("service saw %d requests, want 2", len(reqs))
	}
	first, second := uploadedBatchID(t, reqs[0]), uploadedBatchID(t, reqs[1])
	if first == "" || first != second {
		t.Fatalf("retry batch id = %q, first attempt used %q; they must match", second, first)
	}
	if f.spool.Usage() != 0 {
		t.Error("acknowledged records were not deleted")
	}
}

func TestAnAckNamingAnotherBatchIsNotTrusted(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", fakeplatform.JSON(200, map[string]any{"batch_id": "b-not-ours"}))
	f.storeRawcall(t, "req-1", time.Now().UTC())
	usageBefore := f.spool.Usage()

	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("mismatched acknowledgement was accepted")
	}
	if f.spool.Usage() != usageBefore {
		t.Error("mismatched acknowledgement cost spool records")
	}
}

func TestHandshakeTunesThresholdsQuotaAndSurfaces(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, map[string]any{
		"min_client_version": "2.0.0",
		"flush_bytes":        1,
		"flush_age_seconds":  86400,
		"spool_quota_bytes":  1 << 20,
		"notice":             "an upgrade is available",
	}))
	f.storeRawcall(t, "req-1", time.Now().UTC())
	if _, err := f.uploader.Flush(true); err != nil {
		t.Fatal(err)
	}

	h := upload.LoadHandshake(f.dir)
	if h.MinClientVersion != "2.0.0" || h.Notice != "an upgrade is available" {
		t.Errorf("persisted handshake = %+v", h)
	}
	if got := f.spool.Quota(); got != 1<<20 {
		t.Errorf("spool quota after handshake = %d, want %d", got, 1<<20)
	}

	// The one-byte flush threshold now makes any fresh record enough for
	// an unforced flush.
	f.storeRawcall(t, "req-2", time.Now().UTC())
	res, err := f.uploader.Flush(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.Uploaded {
		t.Fatalf("unforced flush under handshake thresholds = %+v", res)
	}
}

func TestADrainSplitsIntoBoundedBatches(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, map[string]any{"flush_bytes": 1}))
	f.storeRawcall(t, "req-1", time.Now().UTC())
	if _, err := f.uploader.Flush(true); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"req-2", "req-3", "req-4"} {
		f.storeRawcall(t, id, time.Now().UTC())
	}
	res, err := f.uploader.Flush(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Batches != 3 || res.Records != 3 {
		t.Fatalf("result = %+v, want one record per one-byte-capped batch", res)
	}
	if f.spool.Usage() != 0 {
		t.Error("drain left records behind")
	}
}

func TestAPendingBatchWhoseRecordsWereWithdrawnIsReleased(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", fakeplatform.JSON(503, map[string]any{"error": "down"}))
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", time.Now().UTC())

	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("first flush against a failing service did not error")
	}
	if _, err := f.spool.DeleteWhere(func(spool.Rawcall) bool { return true }); err != nil {
		t.Fatal(err)
	}

	res, err := f.uploader.Flush(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.Empty {
		t.Fatalf("flush after withdrawal = %+v", res)
	}
	if got := len(f.server.Requests()); got != 1 {
		t.Errorf("service saw %d requests, want only the failed attempt", got)
	}
}

func TestBatchesCarryRunMetadata(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", time.Now().UTC())
	if _, err := f.uploader.Flush(true); err != nil {
		t.Fatal(err)
	}

	parts, err := fakeplatform.Parts(f.server.Requests()[0])
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Run struct {
			RecordedToday int `json:"recorded_today"`
		} `json:"run"`
	}
	if err := json.Unmarshal(parts["batch"], &env); err != nil {
		t.Fatal(err)
	}
	if env.Run.RecordedToday != 5 {
		t.Errorf("run metadata = %+v", env.Run)
	}
}

func TestATokenSourceFailureStopsTheFlush(t *testing.T) {
	f := newFixture(t)
	broken, err := upload.New(upload.Deps{
		Spool:       f.spool,
		Service:     platform.New(f.server.URL(), "test"),
		DeviceToken: func() (string, error) { return "", errors.New("keyring unavailable") },
		Version:     "1.0.0",
		Dir:         f.dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.storeRawcall(t, "req-1", time.Now().UTC())
	if _, err := broken.Flush(true); err == nil || !strings.Contains(err.Error(), "keyring unavailable") {
		t.Fatalf("flush error = %v, want the token source failure", err)
	}
}

func TestNewRejectsMissingWiring(t *testing.T) {
	if _, err := upload.New(upload.Deps{}); err == nil {
		t.Fatal("an uploader without wiring was built")
	}
}
