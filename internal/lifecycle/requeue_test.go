package lifecycle_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/proxytest"
)

// spooledEnvelope builds valid rawcall bytes as the capture path would
// have spooled them.
func spooledEnvelope(t *testing.T, requestID string, at time.Time) []byte {
	t.Helper()
	return proxytest.Rawcall(t, requestID, "hash-project", at)
}

func TestRequeueMovesABatchBackIntoTheSpool(t *testing.T) {
	e := newEnv(t)
	at := e.deps.Now()
	e.sandbox.QuarantineBatch(
		proxytest.Rejection{BatchID: "b-poison", Details: "413 Request Entity Too Large"},
		map[string][]byte{
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
	e.sandbox.QuarantineBatch(proxytest.Rejection{BatchID: "b-one"},
		map[string][]byte{"req-1": spooledEnvelope(t, "req-1", at)})
	e.sandbox.QuarantineBatch(proxytest.Rejection{BatchID: "b-two"},
		map[string][]byte{"req-2": spooledEnvelope(t, "req-2", at)})

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
	e.sandbox.SeedHandshake(proxytest.Handshake{SpoolQuotaBytes: 1})
	e.sandbox.QuarantineBatch(proxytest.Rejection{BatchID: "b-poison"},
		map[string][]byte{"req-1": spooledEnvelope(t, "req-1", e.deps.Now())})

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
	e.sandbox.QuarantineBatch(proxytest.Rejection{BatchID: "b-a-stuck"},
		map[string][]byte{"req-bad": []byte("not an envelope")})
	e.sandbox.QuarantineBatch(proxytest.Rejection{BatchID: "b-b-good"},
		map[string][]byte{"req-1": spooledEnvelope(t, "req-1", at)})

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
	e.sandbox.QuarantineBatch(proxytest.Rejection{BatchID: "b-poison"}, map[string][]byte{
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
	quarantined := e.sandbox.QuarantinedBatches()
	if len(quarantined) != 1 || quarantined[0].Records != 1 {
		t.Fatalf("quarantine = %+v, want the unreadable record still set aside", quarantined)
	}
	// The reason travels with whatever stays: a record left behind
	// without one can never be explained to the user again.
	if quarantined[0].Reason.BatchID != "b-poison" {
		t.Errorf("quarantined batch = %+v, want its recorded reason kept", quarantined[0])
	}
}
