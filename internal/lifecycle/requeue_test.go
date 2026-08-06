package lifecycle_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
)

// seedRejectedBatch quarantines records under one batch id, the way a
// service rejection would have left them.
func seedRejectedBatch(t *testing.T, e *env, batchID, details string, records map[string][]byte) {
	t.Helper()
	dir := filepath.Join(e.layout().RejectedDir(), batchID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for id, data := range records {
		if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reason, err := json.Marshal(map[string]any{
		"batch_id": batchID, "records": len(records), "details": details,
		"at": "2026-08-02T09:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reason.json"), reason, 0o600); err != nil {
		t.Fatal(err)
	}
}

// spooledEnvelope builds valid rawcall bytes as the capture path would
// have spooled them.
func spooledEnvelope(t *testing.T, requestID string, at time.Time) []byte {
	t.Helper()
	env, err := envelope.Record(envelope.Observation{
		Provider: "anthropic", Endpoint: "/v1/messages", HTTPStatus: 200,
		ProjectIDHash: "hash-project", At: at,
		Request:     []byte(`{"model":"claude-fable-5"}`),
		Response:    []byte(`{"id":"` + requestID + `"}`),
		ContentType: "application/json", RequestComplete: true, ResponseComplete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return env.Bytes()
}

func TestRequeueMovesABatchBackIntoTheSpool(t *testing.T) {
	e := newEnv(t)
	at := e.deps.Now()
	seedRejectedBatch(t, e, "b-poison", "413 Request Entity Too Large", map[string][]byte{
		"req-1": spooledEnvelope(t, "req-1", at),
		"req-2": spooledEnvelope(t, "req-2", at),
	})

	if err := e.machine().RequeueRejected("b-poison", false, e.io()); err != nil {
		t.Fatalf("requeue: %v\nstdout: %s", err, e.stdout)
	}

	if got := len(e.sandbox.Rawcalls()); got != 2 {
		t.Errorf("spool holds %d rawcall(s) after requeue, want 2", got)
	}
	if _, err := os.Stat(filepath.Join(e.layout().RejectedDir(), "b-poison")); !os.IsNotExist(err) {
		t.Error("rejected batch directory still present after requeue")
	}
	out := e.stdout.String()
	for _, want := range []string{"2 rawcall(s)", "b-poison", "413 Request Entity Too Large", "`trajector upload --force`"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want it to contain %q", out, want)
		}
	}
}

func TestRequeueAllHandlesEveryBatch(t *testing.T) {
	e := newEnv(t)
	at := e.deps.Now()
	seedRejectedBatch(t, e, "b-one", "", map[string][]byte{"req-1": spooledEnvelope(t, "req-1", at)})
	seedRejectedBatch(t, e, "b-two", "", map[string][]byte{"req-2": spooledEnvelope(t, "req-2", at)})

	if err := e.machine().RequeueRejected("", true, e.io()); err != nil {
		t.Fatalf("requeue --all: %v\nstdout: %s", err, e.stdout)
	}
	if got := len(e.sandbox.Rawcalls()); got != 2 {
		t.Errorf("spool holds %d rawcall(s), want 2", got)
	}
	if entries, err := os.ReadDir(e.layout().RejectedDir()); err == nil && len(entries) != 0 {
		t.Errorf("rejected store not empty after requeue --all: %v", entries)
	}
}

func TestRequeueAllWithNothingQuarantinedSaysSo(t *testing.T) {
	e := newEnv(t)
	if err := e.machine().RequeueRejected("", true, e.io()); err != nil {
		t.Fatalf("requeue --all on empty store: %v", err)
	}
	if !strings.Contains(e.stdout.String(), "No rejected batches") {
		t.Errorf("output = %q, want it to say there is nothing to requeue", e.stdout)
	}
}

func TestRequeueIsNotRefusedByTheQuota(t *testing.T) {
	e := newEnv(t)
	// A quota the batch clearly exceeds: quarantined data already lived
	// in the spool once and must be allowed back to be uploaded at all.
	writeUploadFile(t, e, "handshake.json", map[string]any{"spool_quota_bytes": 1})
	seedRejectedBatch(t, e, "b-poison", "", map[string][]byte{
		"req-1": spooledEnvelope(t, "req-1", e.deps.Now()),
	})

	if err := e.machine().RequeueRejected("b-poison", false, e.io()); err != nil {
		t.Fatalf("requeue against a full quota: %v", err)
	}
	if got := len(e.sandbox.Rawcalls()); got != 1 {
		t.Errorf("spool holds %d rawcall(s), want 1", got)
	}
}

func TestRequeueAllContinuesPastAStuckBatch(t *testing.T) {
	e := newEnv(t)
	at := e.deps.Now()
	// Batch names sort the stuck one first: the batches behind it must
	// still be attempted.
	seedRejectedBatch(t, e, "b-a-stuck", "", map[string][]byte{"req-bad": []byte("not an envelope")})
	seedRejectedBatch(t, e, "b-b-good", "", map[string][]byte{"req-1": spooledEnvelope(t, "req-1", at)})

	err := e.machine().RequeueRejected("", true, e.io())
	if err == nil || !strings.Contains(err.Error(), "req-bad") {
		t.Fatalf("err = %v, want the stuck record reported", err)
	}
	if got := len(e.sandbox.Rawcalls()); got != 1 {
		t.Errorf("spool holds %d rawcall(s), want the batch behind the stuck one moved", got)
	}
	if _, statErr := os.Stat(filepath.Join(e.layout().RejectedDir(), "b-b-good")); !os.IsNotExist(statErr) {
		t.Error("the healthy batch should be gone from quarantine")
	}
}

func TestRequeueUnknownBatchFails(t *testing.T) {
	e := newEnv(t)
	err := e.machine().RequeueRejected("b-missing", false, e.io())
	if err == nil || !strings.Contains(err.Error(), "b-missing") {
		t.Fatalf("err = %v, want the unknown batch named", err)
	}
}

func TestRequeueLeavesAnUnreadableRecordQuarantined(t *testing.T) {
	e := newEnv(t)
	at := e.deps.Now()
	seedRejectedBatch(t, e, "b-poison", "", map[string][]byte{
		"req-good": spooledEnvelope(t, "req-good", at),
		"req-bad":  []byte("not an envelope"),
	})

	err := e.machine().RequeueRejected("b-poison", false, e.io())
	if err == nil {
		t.Fatal("requeue succeeded despite an unreadable record")
	}
	if got := len(e.sandbox.Rawcalls()); got != 1 {
		t.Errorf("spool holds %d rawcall(s), want the readable one moved", got)
	}
	dir := filepath.Join(e.layout().RejectedDir(), "b-poison")
	if _, statErr := os.Stat(filepath.Join(dir, "req-bad.json")); statErr != nil {
		t.Error("the unreadable record must stay quarantined, not vanish")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "reason.json")); statErr != nil {
		t.Error("reason.json must stay while any record remains")
	}
}
