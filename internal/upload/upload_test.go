package upload_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	spoolDir string
	server   *fakeplatform.Server
	uploader *upload.Uploader
	dir      string
	rejected string
	token    string
	// logs collects what the uploader reports through its Logf seam.
	logs strings.Builder
	// now is the uploader's clock; tests advance it to cross gates.
	now time.Time
	// withdrawn stands in for the consent store: project hashes the user
	// has since disabled.
	withdrawn map[string]bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		server:    fakeplatform.New(t),
		dir:       filepath.Join(t.TempDir(), "upload"),
		rejected:  filepath.Join(t.TempDir(), "rawcalls-rejected"),
		spoolDir:  filepath.Join(t.TempDir(), "rawcalls"),
		token:     "dev-tok-fake",
		now:       time.Now().UTC(),
		withdrawn: map[string]bool{},
	}
	sp, err := spool.Create(f.spoolDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.spool = sp
	u, err := upload.New(upload.Deps{
		Spool:       sp,
		Service:     platform.New(f.server.URL(), "test"),
		DeviceToken: func() (string, error) { return f.token, nil },
		Withdrawn:   func(hash string) bool { return f.withdrawn[hash] },
		Version:     "1.0.0",
		Dir:         f.dir,
		RejectedDir: f.rejected,
		Run: func() batch.Run {
			return batch.Run{RecordedToday: 5, SpoolUsageBytes: sp.Usage(), SpoolQuotaBytes: sp.Quota()}
		},
		Logf: func(format string, args ...any) { fmt.Fprintf(&f.logs, format+"\n", args...) },
		Now:  func() time.Time { return f.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.uploader = u
	return f
}

// tearStoredRawcall truncates a stored rawcall's file to its front
// half, leaving what a write torn by a crash leaves, and returns the
// bytes that remain on disk.
func (f *fixture) tearStoredRawcall(t *testing.T, id string) []byte {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(f.spoolDir, "*", id+".json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("locating the stored rawcall %s: %v (matches: %v)", id, err, matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	torn := data[:len(data)/2]
	if err := os.WriteFile(matches[0], torn, 0o600); err != nil {
		t.Fatal(err)
	}
	return torn
}

func (f *fixture) storeRawcall(t *testing.T, id string, at time.Time) {
	t.Helper()
	f.storeRawcallFor(t, id, "hash-p1", at)
}

func (f *fixture) storeRawcallFor(t *testing.T, id, projectIDHash string, at time.Time) {
	t.Helper()
	env, err := envelope.Record(envelope.Observation{
		Provider:          "anthropic",
		Endpoint:          "/v1/messages",
		HTTPStatus:        200,
		ClientVersion:     "test",
		ProjectIDHash:     projectIDHash,
		At:                at,
		Upstream:          "https://api.anthropic.com",
		OfficialUpstream:  "https://api.anthropic.com",
		Request:           []byte(`{"model":"m","metadata":{"user_id":"sess-1"},"messages":[{"role":"user","content":"hello"}]}`),
		RequestComplete:   true,
		Response:          []byte(`{"id":"` + id + `","type":"message"}`),
		ResponseComplete:  true,
		ContentType:       "application/json",
		UpstreamRequestID: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.spool.Write(env); err != nil {
		t.Fatal(err)
	}
}

// echoAck acknowledges whatever batch id the request carries, plus any
// handshake fields.
//
// TODO: converge the three sibling ack builders (echoAck here,
// stubEchoAck in cli, ackBatch in lifecycle) into fakeplatform the
// next time ack semantics change.
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
	if _, err := f.spool.DeleteWhere(func(string) bool { return true }); err != nil {
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
		RejectedDir: f.rejected,
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

func TestCloseRunsAFinalFlushAndThenRefusesToFlush(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", time.Now().UTC().Add(-25*time.Hour))

	if err := f.uploader.Close(time.Minute); err != nil {
		t.Fatal(err)
	}
	if f.spool.Usage() != 0 {
		t.Error("the final flush left aged records in the spool")
	}
	if f.uploadCount() != 1 {
		t.Fatalf("service saw %d requests, want 1", f.uploadCount())
	}

	f.storeRawcall(t, "req-2", time.Now().UTC())
	if _, err := f.uploader.Flush(true); !errors.Is(err, upload.ErrClosed) {
		t.Fatalf("flush after close = %v, want ErrClosed", err)
	}
	if err := f.uploader.Close(time.Minute); err != nil {
		t.Errorf("second close = %v, want nil", err)
	}
	if f.uploadCount() != 1 {
		t.Errorf("service saw %d requests after close, want still 1", f.uploadCount())
	}
	if f.spool.Usage() == 0 {
		t.Error("a record stored after close was deleted")
	}
}

func TestCloseKeepsRecordsBelowTheUploadThresholds(t *testing.T) {
	f := newFixture(t)
	f.storeRawcall(t, "req-1", time.Now().UTC())

	if err := f.uploader.Close(time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := len(f.server.Requests()); got != 0 {
		t.Errorf("service saw %d requests, want 0", got)
	}
	if f.spool.Usage() == 0 {
		t.Error("close drained a spool below the thresholds")
	}
}

func TestCloseAbandonsAnUploadThatOutlastsItsBudget(t *testing.T) {
	f := newFixture(t)
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })
	f.server.StubFunc("POST", "/v1/batches", func(fakeplatform.Request) fakeplatform.Response {
		select {
		case <-released:
		case <-time.After(10 * time.Second):
		}
		return fakeplatform.JSON(200, map[string]any{"batch_id": "never-acknowledged"})
	})
	f.storeRawcall(t, "req-1", time.Now().UTC().Add(-25*time.Hour))

	const budget = 200 * time.Millisecond
	start := time.Now()
	err := f.uploader.Close(budget)
	spent := time.Since(start)

	if spent > 30*budget {
		t.Errorf("close ran %s on a %s budget, want the upload abandoned when the budget ran out", spent, budget)
	}
	if err == nil {
		t.Error("close = nil, want the abandoned upload reported")
	}
	if f.spool.Usage() == 0 {
		t.Error("the abandoned upload's records left the spool")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "pending.json")); err != nil {
		t.Errorf("the abandoned batch left no pending record for the next flusher: %v", err)
	}
}

func rejectStub(status int, detail string) fakeplatform.Response {
	return fakeplatform.JSON(status, map[string]any{"error": detail})
}

func (f *fixture) uploadCount() int {
	return len(f.server.Requests())
}

func TestARejectedBatchIsQuarantinedAndUnblocksUploads(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", rejectStub(400, "bad multipart"))
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", time.Now().UTC())

	_, err := f.uploader.Flush(true)
	if err == nil || !strings.Contains(err.Error(), "moved to") {
		t.Fatalf("flush error = %v, want a rejection notice naming the rejected dir", err)
	}
	if f.spool.Usage() != 0 {
		t.Fatal("rejected records were left in the spool")
	}
	if n := rejectedRecords(t, f.rejected); n != 1 {
		t.Fatalf("rejected store holds %d records, want 1", n)
	}
	st := upload.LoadState(f.dir)
	if st.LastRejected == nil || st.LastRejected.Records != 1 || !strings.Contains(st.LastRejected.Details, "bad multipart") {
		t.Fatalf("state.LastRejected = %+v", st.LastRejected)
	}
	batchDir := filepath.Join(f.rejected, st.LastRejected.BatchID)
	if _, err := os.Stat(filepath.Join(batchDir, "req-1.json")); err != nil {
		t.Errorf("quarantined record missing: %v", err)
	}
	var reason upload.Rejection
	raw, err := os.ReadFile(filepath.Join(batchDir, "reason.json"))
	if err != nil {
		t.Fatalf("reason file missing: %v", err)
	}
	if err := json.Unmarshal(raw, &reason); err != nil || reason.BatchID != st.LastRejected.BatchID {
		t.Errorf("reason = %+v (%v)", reason, err)
	}

	f.storeRawcall(t, "req-2", time.Now().UTC())
	res, err := f.uploader.Flush(true)
	if err != nil || res.Outcome != upload.Uploaded {
		t.Fatalf("flush after quarantine = %+v, %v; the queue must be unblocked", res, err)
	}
	if f.uploadCount() != 2 {
		t.Errorf("service saw %d requests, want 2", f.uploadCount())
	}
}

func TestARejectedBatchIsNotRetriedAutomatically(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", rejectStub(422, "unacceptable"))
	f.storeRawcall(t, "req-1", time.Now().UTC())

	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("rejection did not surface")
	}
	res, err := f.uploader.Flush(false)
	if err != nil || res.Outcome != upload.Empty {
		t.Fatalf("flush after quarantine = %+v, %v", res, err)
	}
	if f.uploadCount() != 1 {
		t.Errorf("service saw %d requests, want only the rejected attempt", f.uploadCount())
	}
}

func TestAClassifiedFailureCarriesItsOutcomeAlongsideTheError(t *testing.T) {
	limited := fakeplatform.JSON(429, map[string]any{})
	limited.Header.Set("Retry-After", "120")
	cases := []struct {
		name string
		stub fakeplatform.Response
		want upload.Outcome
	}{
		{"upgrade required", fakeplatform.JSON(426, map[string]any{"min_client_version": "9.9.9"}), upload.UpgradeRequired},
		{"deferred", limited, upload.Deferred},
		{"rejected", rejectStub(400, "bad multipart"), upload.Rejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.server.Stub("POST", "/v1/batches", tc.stub)
			f.storeRawcall(t, "req-1", time.Now().UTC())

			res, err := f.uploader.Flush(true)
			if err == nil {
				t.Fatal("the refusal did not surface as an error")
			}
			if res.Outcome != tc.want {
				t.Errorf("outcome = %q, want %q", res.Outcome, tc.want)
			}
			if tc.want == upload.UpgradeRequired && res.MinClientVersion != "9.9.9" {
				t.Errorf("min client version = %q, want it carried with the outcome", res.MinClientVersion)
			}
		})
	}
}

func TestAnUnclassifiedFailureCarriesNoOutcome(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", fakeplatform.JSON(503, map[string]any{"error": "down"}))
	f.storeRawcall(t, "req-1", time.Now().UTC())

	res, err := f.uploader.Flush(true)
	if err == nil {
		t.Fatal("the failure did not surface as an error")
	}
	if res.Outcome != "" {
		t.Errorf("outcome = %q, want none for a failure with no class", res.Outcome)
	}
}

func TestAnUpgradeGatePausesAutomaticFlushes(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", fakeplatform.JSON(426, map[string]any{"min_client_version": "9.9.9"}))
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", time.Now().UTC())

	if _, err := f.uploader.Flush(true); err == nil || !strings.Contains(err.Error(), "9.9.9") {
		t.Fatalf("426 flush error = %v, want the required version", err)
	}
	if f.spool.Usage() == 0 {
		t.Fatal("data was touched by a version refusal")
	}
	if n := rejectedRecords(t, f.rejected); n != 0 {
		t.Fatal("a version refusal quarantined valid data")
	}
	if got := upload.LoadHandshake(f.dir).MinClientVersion; got != "9.9.9" {
		t.Errorf("persisted min client version = %q", got)
	}

	res, err := f.uploader.Flush(false)
	if err != nil || res.Outcome != upload.UpgradeRequired {
		t.Fatalf("automatic flush under the gate = %+v, %v", res, err)
	}
	if f.uploadCount() != 1 {
		t.Fatalf("automatic flush uploaded despite the gate")
	}

	res, err = f.uploader.Flush(true)
	if err != nil || res.Outcome != upload.Uploaded {
		t.Fatalf("forced flush past the gate = %+v, %v", res, err)
	}
	if res2, err := f.uploader.Flush(false); err != nil || res2.Outcome != upload.Empty {
		t.Fatalf("flush after a success = %+v, %v; the gate must have lifted", res2, err)
	}
}

func TestAVersionRefusalRelaysWhatTheServiceSaid(t *testing.T) {
	// A required version alone tells the user they are behind; the
	// service's own sentence tells them why and by when, which is the
	// part no local wording can supply.
	const said = "Upload format 0.1.x is retired on 2026-09-01."
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", fakeplatform.JSON(426, map[string]any{
		"min_client_version": "9.9.9",
		"message":            said,
	}))
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", f.now)

	res, err := f.uploader.Flush(true)
	if err == nil {
		t.Fatal("426 did not surface")
	}
	if res.UpgradeMessage != said {
		t.Errorf("upgrade message on the refusal = %q, want %q", res.UpgradeMessage, said)
	}
	if got := upload.LoadUpgradeMessage(f.dir); got != said {
		t.Errorf("persisted upgrade message = %q, want %q", got, said)
	}

	// A later process — status, doctor — reads the message from disk;
	// so does the gate that answers without asking the service again.
	res, err = f.uploader.Flush(false)
	if err != nil || res.Outcome != upload.UpgradeRequired {
		t.Fatalf("gated flush = %+v, %v", res, err)
	}
	if res.UpgradeMessage != said {
		t.Errorf("gated flush message = %q, want the service's words repeated", res.UpgradeMessage)
	}

	// An acknowledged upload is the service accepting this version, so
	// the refusal it once made is no longer true and must stop showing.
	res, err = f.uploader.Flush(true)
	if err != nil || res.Outcome != upload.Uploaded {
		t.Fatalf("forced flush past the gate = %+v, %v", res, err)
	}
	if res.UpgradeMessage != "" {
		t.Errorf("message after a success = %q, want none", res.UpgradeMessage)
	}
	if got := upload.LoadUpgradeMessage(f.dir); got != "" {
		t.Errorf("persisted message after a success = %q, want none", got)
	}
}

func TestAVersionRefusalWithoutWordsPersistsNone(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", fakeplatform.JSON(426, map[string]any{"min_client_version": "9.9.9"}))
	f.storeRawcall(t, "req-1", f.now)

	res, _ := f.uploader.Flush(true)
	if res.MinClientVersion != "9.9.9" {
		t.Fatalf("refusal = %+v, want the required version", res)
	}
	if res.UpgradeMessage != "" || upload.LoadUpgradeMessage(f.dir) != "" {
		t.Errorf("a silent refusal produced words: %q / %q", res.UpgradeMessage, upload.LoadUpgradeMessage(f.dir))
	}
}

func TestAServiceMessageOnDiskCannotDrawOnTheTerminal(t *testing.T) {
	// The message is service-controlled text that a later process
	// prints beside our own lines. It is disarmed on the way in and on
	// the way out, so a file edited between the two still cannot forge
	// a line of our output.
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", fakeplatform.JSON(426, map[string]any{
		"min_client_version": "9.9.9",
		"message":            "upgrade now\rtrajector: everything is fine",
	}))
	f.storeRawcall(t, "req-1", f.now)

	res, _ := f.uploader.Flush(true)
	for _, got := range []string{res.UpgradeMessage, upload.LoadUpgradeMessage(f.dir)} {
		if strings.ContainsAny(got, "\r\n\x1b") {
			t.Errorf("relayed message = %q, want the terminal control runes gone", got)
		}
		if !strings.Contains(got, "upgrade now") {
			t.Errorf("relayed message = %q, want the words kept", got)
		}
	}
}

func TestUpgradeMessageIsAbsentBeforeAnyRefusal(t *testing.T) {
	f := newFixture(t)
	if got := upload.LoadUpgradeMessage(f.dir); got != "" {
		t.Errorf("upgrade message with no handshake on disk = %q, want none", got)
	}
}

func TestRateLimitingDefersAutomaticFlushes(t *testing.T) {
	f := newFixture(t)
	limited := fakeplatform.JSON(429, map[string]any{})
	limited.Header.Set("Retry-After", "120")
	f.server.Stub("POST", "/v1/batches", limited)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", time.Now().UTC().Add(-25*time.Hour))

	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("429 did not surface")
	}
	res, err := f.uploader.Flush(false)
	if err != nil || res.Outcome != upload.Deferred {
		t.Fatalf("automatic flush during the pause = %+v, %v", res, err)
	}
	if f.uploadCount() != 1 {
		t.Fatal("automatic flush uploaded during the pause")
	}

	f.now = f.now.Add(121 * time.Second)
	res, err = f.uploader.Flush(false)
	if err != nil || res.Outcome != upload.Uploaded {
		t.Fatalf("flush after the pause = %+v, %v", res, err)
	}
}

func TestAForcedFlushIgnoresTheRateLimitPause(t *testing.T) {
	f := newFixture(t)
	limited := fakeplatform.JSON(429, map[string]any{})
	limited.Header.Set("Retry-After", "3600")
	f.server.Stub("POST", "/v1/batches", limited)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", time.Now().UTC())

	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("429 did not surface")
	}
	res, err := f.uploader.Flush(true)
	if err != nil || res.Outcome != upload.Uploaded {
		t.Fatalf("forced flush during the pause = %+v, %v", res, err)
	}
}

func TestTheRetryAfterPauseIsCapped(t *testing.T) {
	f := newFixture(t)
	limited := fakeplatform.JSON(429, map[string]any{})
	limited.Header.Set("Retry-After", "86400")
	f.server.Stub("POST", "/v1/batches", limited)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", time.Now().UTC().Add(-25*time.Hour))

	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("429 did not surface")
	}
	f.now = f.now.Add(time.Hour + time.Second)
	res, err := f.uploader.Flush(false)
	if err != nil || res.Outcome != upload.Uploaded {
		t.Fatalf("flush an hour into a day-long pause = %+v, %v; the cap must apply", res, err)
	}
}

func TestARateLimitWithoutRetryAfterDefersAutomaticFlushes(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", fakeplatform.JSON(429, map[string]any{}))
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", time.Now().UTC().Add(-25*time.Hour))

	res, err := f.uploader.Flush(true)
	if err == nil {
		t.Fatal("429 did not surface")
	}
	if res.Outcome != upload.Deferred {
		t.Fatalf("outcome = %q, want %q", res.Outcome, upload.Deferred)
	}
	if !strings.Contains(err.Error(), "wait") {
		t.Errorf("flush error = %q, want the default pause named", err)
	}

	f.now = f.now.Add(4 * time.Minute)
	res, err = f.uploader.Flush(false)
	if err != nil || res.Outcome != upload.Deferred {
		t.Fatalf("automatic flush four minutes into the pause = %+v, %v", res, err)
	}
	if f.uploadCount() != 1 {
		t.Fatalf("service saw %d requests during the pause, want 1", f.uploadCount())
	}

	f.now = f.now.Add(time.Minute + time.Second)
	res, err = f.uploader.Flush(false)
	if err != nil || res.Outcome != upload.Uploaded {
		t.Fatalf("flush after the pause = %+v, %v", res, err)
	}
}

func TestAnAckOmittingASettingLeavesTheStoredValueAlone(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, map[string]any{
		"min_client_version": "1.5.0",
		"flush_bytes":        1,
		"flush_age_seconds":  3600,
		"spool_quota_bytes":  1 << 20,
		"notice":             "an upgrade is available",
	}))
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, map[string]any{"spool_quota_bytes": 2 << 20}))
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	want := platform.Handshake{
		MinClientVersion: "1.5.0",
		FlushBytes:       1,
		FlushAgeSeconds:  3600,
		SpoolQuotaBytes:  2 << 20,
		Notice:           "an upgrade is available",
	}

	f.storeRawcall(t, "req-1", time.Now().UTC())
	if _, err := f.uploader.Flush(true); err != nil {
		t.Fatal(err)
	}
	f.storeRawcall(t, "req-2", time.Now().UTC())
	if _, err := f.uploader.Flush(true); err != nil {
		t.Fatal(err)
	}
	if h := upload.LoadHandshake(f.dir); h != want {
		t.Errorf("handshake after a quota-only ack = %+v, want %+v", h, want)
	}
	if got := f.spool.Quota(); got != 2<<20 {
		t.Errorf("spool quota after a quota-only ack = %d, want %d", got, 2<<20)
	}

	f.storeRawcall(t, "req-3", time.Now().UTC())
	if _, err := f.uploader.Flush(true); err != nil {
		t.Fatal(err)
	}
	if h := upload.LoadHandshake(f.dir); h != want {
		t.Errorf("handshake after an empty ack = %+v, want %+v", h, want)
	}
	if got := f.spool.Quota(); got != 2<<20 {
		t.Errorf("spool quota after an empty ack = %d, want %d", got, 2<<20)
	}
}

func TestAVersionRefusalPreservesTheRestOfTheStoredHandshake(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, map[string]any{
		"flush_bytes":       1,
		"spool_quota_bytes": 1 << 20,
		"notice":            "an upgrade is available",
	}))
	f.server.Stub("POST", "/v1/batches", fakeplatform.JSON(426, map[string]any{"min_client_version": "9.9.9"}))

	f.storeRawcall(t, "req-1", time.Now().UTC())
	if _, err := f.uploader.Flush(true); err != nil {
		t.Fatal(err)
	}
	f.storeRawcall(t, "req-2", time.Now().UTC())
	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("426 did not surface")
	}

	want := platform.Handshake{
		MinClientVersion: "9.9.9",
		FlushBytes:       1,
		SpoolQuotaBytes:  1 << 20,
		Notice:           "an upgrade is available",
	}
	if h := upload.LoadHandshake(f.dir); h != want {
		t.Errorf("handshake after the version refusal = %+v, want %+v", h, want)
	}
}

func timeoutStub() fakeplatform.Response {
	return fakeplatform.JSON(408, map[string]any{})
}

func TestConsecutiveTimeoutsBackOffAndRetryTheSameBatch(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", timeoutStub())
	f.server.Stub("POST", "/v1/batches", timeoutStub())
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", time.Now().UTC())

	_, err := f.uploader.Flush(true)
	if err == nil {
		t.Fatal("timed-out upload did not surface")
	}
	msg := err.Error()
	if !strings.Contains(msg, "B)") || !strings.Contains(msg, "waits") {
		t.Fatalf("flush error = %q, want the batch size and the retry plan", msg)
	}
	if strings.Contains(msg, "context deadline exceeded") {
		t.Fatalf("flush error = %q surfaces a bare deadline", msg)
	}
	if st := upload.LoadState(f.dir); !strings.Contains(st.LastError, "B)") {
		t.Errorf("state.LastError = %q, want the batch size", st.LastError)
	}

	res, err := f.uploader.Flush(false)
	if err != nil || res.Outcome != upload.Deferred {
		t.Fatalf("automatic flush right after a timeout = %+v, %v, want %q", res, err, upload.Deferred)
	}
	if f.uploadCount() != 1 {
		t.Fatalf("service saw %d requests during the pause, want 1", f.uploadCount())
	}

	f.now = f.now.Add(61 * time.Second)
	if _, err := f.uploader.Flush(false); err == nil {
		t.Fatal("second timed-out attempt did not surface")
	}
	if f.uploadCount() != 2 {
		t.Fatalf("service saw %d requests after the pause, want 2", f.uploadCount())
	}

	f.now = f.now.Add(61 * time.Second)
	res, err = f.uploader.Flush(false)
	if err != nil || res.Outcome != upload.Deferred {
		t.Fatalf("flush one minute into a doubled pause = %+v, %v, want %q", res, err, upload.Deferred)
	}

	f.now = f.now.Add(60 * time.Second)
	res, err = f.uploader.Flush(false)
	if err != nil || res.Outcome != upload.Uploaded {
		t.Fatalf("flush after the doubled pause = %+v, %v", res, err)
	}
	reqs := f.server.Requests()
	if len(reqs) != 3 {
		t.Fatalf("service saw %d requests, want 3", len(reqs))
	}
	first := uploadedBatchID(t, reqs[0])
	for i, r := range reqs[1:] {
		if id := uploadedBatchID(t, r); id != first {
			t.Fatalf("attempt %d used batch id %q, first attempt used %q; they must match", i+2, id, first)
		}
	}
	if f.spool.Usage() != 0 {
		t.Error("acknowledged records were not deleted")
	}
}

func TestAnAcknowledgedUploadResetsTheTimeoutEscalation(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", timeoutStub())
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.server.Stub("POST", "/v1/batches", timeoutStub())
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", time.Now().UTC())

	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("timed-out upload did not surface")
	}
	res, err := f.uploader.Flush(true)
	if err != nil || res.Outcome != upload.Uploaded {
		t.Fatalf("forced flush during the timeout pause = %+v, %v; force must not wait", res, err)
	}

	f.storeRawcall(t, "req-2", time.Now().UTC())
	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("timed-out upload after a success did not surface")
	}
	f.now = f.now.Add(61 * time.Second)
	res, err = f.uploader.Flush(false)
	if err != nil || res.Outcome != upload.Uploaded {
		t.Fatalf("flush a minute after a fresh timeout = %+v, %v; the pause must restart from its base", res, err)
	}
}

func TestPurgeRejectedRemovesOnlyThatProject(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", rejectStub(400, "nope"))
	f.storeRawcall(t, "req-1", time.Now().UTC())
	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("rejection did not surface")
	}

	if n, err := upload.PurgeRejected(f.rejected, "hash-other"); err != nil || n != 0 {
		t.Fatalf("purging another project = %d, %v", n, err)
	}
	if n := rejectedRecords(t, f.rejected); n != 1 {
		t.Fatal("another project's purge touched the record")
	}

	if n, err := upload.PurgeRejected(f.rejected, "hash-p1"); err != nil || n != 1 {
		t.Fatalf("purging the project = %d, %v", n, err)
	}
	if n := rejectedRecords(t, f.rejected); n != 0 {
		t.Fatal("the record survived its project's purge")
	}
	entries, err := os.ReadDir(f.rejected)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("emptied batch directory was kept: %v", entries)
	}
}
