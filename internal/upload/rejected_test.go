package upload_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

func seedBatch(t *testing.T, rejectedDir, batchID string, records map[string][]byte) {
	t.Helper()
	dir := filepath.Join(rejectedDir, batchID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for id, data := range records {
		if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reason, err := json.Marshal(map[string]any{
		"batch_id": batchID, "records": len(records),
		"details": "400 Bad Request", "at": "2026-08-02T09:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reason.json"), reason, 0o600); err != nil {
		t.Fatal(err)
	}
}

func rawcallBytes(t *testing.T, requestID string) []byte {
	t.Helper()
	env, err := envelope.Record(envelope.Observation{
		Provider: "anthropic", Endpoint: "/v1/messages", HTTPStatus: 200,
		ProjectIDHash: "hash-project", At: time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC),
		Request:     []byte(`{"model":"claude-fable-5"}`),
		Response:    []byte(`{"id":"` + requestID + `"}`),
		ContentType: "application/json", RequestComplete: true, ResponseComplete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return env.Bytes()
}

func spooledCount(t *testing.T, sp *spool.Spool) int {
	t.Helper()
	count := 0
	if err := sp.Each(func(spool.Rawcall) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestRequeueMovesEveryRecordAndRemovesTheBatch(t *testing.T) {
	rejectedDir, sp := t.TempDir(), openSpool(t)
	seedBatch(t, rejectedDir, "b-poison", map[string][]byte{
		"req-1": rawcallBytes(t, "req-1"),
		"req-2": rawcallBytes(t, "req-2"),
	})

	rej, moved, err := upload.Requeue(rejectedDir, sp, "b-poison")
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if moved != 2 {
		t.Errorf("moved = %d, want 2", moved)
	}
	if rej.Details != "400 Bad Request" {
		t.Errorf("reason details = %q, want the recorded rejection returned", rej.Details)
	}
	if got := spooledCount(t, sp); got != 2 {
		t.Errorf("spool holds %d record(s), want 2", got)
	}
	if _, err := os.Stat(filepath.Join(rejectedDir, "b-poison")); !os.IsNotExist(err) {
		t.Error("batch directory still present after a full requeue")
	}
}

func TestRequeueUnknownBatchNamesIt(t *testing.T) {
	_, _, err := upload.Requeue(t.TempDir(), openSpool(t), "b-missing")
	if err == nil || !strings.Contains(err.Error(), "b-missing") {
		t.Fatalf("err = %v, want the unknown batch named", err)
	}
}

func TestRequeueKeepsAnUnreadableRecordQuarantined(t *testing.T) {
	rejectedDir, sp := t.TempDir(), openSpool(t)
	seedBatch(t, rejectedDir, "b-poison", map[string][]byte{
		"req-good": rawcallBytes(t, "req-good"),
		"req-bad":  []byte("not an envelope"),
	})

	_, moved, err := upload.Requeue(rejectedDir, sp, "b-poison")
	if err == nil || !strings.Contains(err.Error(), "req-bad") {
		t.Fatalf("err = %v, want the stuck record named", err)
	}
	if moved != 1 {
		t.Errorf("moved = %d, want the readable record moved regardless", moved)
	}
	dir := filepath.Join(rejectedDir, "b-poison")
	for _, keep := range []string{"req-bad.json", "reason.json"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s must survive a partial requeue: %v", keep, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "req-good.json")); !os.IsNotExist(err) {
		t.Error("the moved record must leave the quarantine")
	}
}

func TestDiscardRemovesTheBatchAndCountsItsRecords(t *testing.T) {
	rejectedDir := t.TempDir()
	seedBatch(t, rejectedDir, "b-poison", map[string][]byte{
		"req-good": rawcallBytes(t, "req-good"),
		"req-bad":  []byte("not an envelope"),
	})

	rej, deleted, err := upload.Discard(rejectedDir, "b-poison")
	if err != nil {
		t.Fatalf("discard: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want every record counted whether or not it reads back", deleted)
	}
	if rej.Details != "400 Bad Request" {
		t.Errorf("reason details = %q, want the recorded rejection returned", rej.Details)
	}
	if _, err := os.Stat(filepath.Join(rejectedDir, "b-poison")); !os.IsNotExist(err) {
		t.Error("batch directory still present after discard")
	}
}

func TestDiscardUnknownBatchNamesIt(t *testing.T) {
	_, _, err := upload.Discard(t.TempDir(), "b-missing")
	if err == nil || !strings.Contains(err.Error(), "b-missing") {
		t.Fatalf("err = %v, want the unknown batch named", err)
	}
}

func TestBatchIdsThatLeaveTheStoreAreRefused(t *testing.T) {
	rejectedDir := t.TempDir()
	sibling := filepath.Join(rejectedDir, "..", "sibling")
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"..", filepath.Join("..", "sibling"), ""} {
		if _, _, err := upload.Discard(rejectedDir, id); err == nil {
			t.Errorf("discard %q was accepted", id)
		}
		if _, _, err := upload.Requeue(rejectedDir, openSpool(t), id); err == nil {
			t.Errorf("requeue %q was accepted", id)
		}
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("a directory outside the store was removed: %v", err)
	}
}

func TestListRejectedReportsCountsAndReasons(t *testing.T) {
	rejectedDir := t.TempDir()
	seedBatch(t, rejectedDir, "b-one", map[string][]byte{"req-1": []byte(`{}`)})
	seedBatch(t, rejectedDir, "b-two", map[string][]byte{"req-2": []byte(`{}`), "req-3": []byte(`{}`)})
	// A stray file at the top level is not a batch.
	if err := os.WriteFile(filepath.Join(rejectedDir, "stray.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	batches, err := upload.ListRejected(rejectedDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 {
		t.Fatalf("batches = %d, want 2", len(batches))
	}
	if batches[0].BatchID != "b-one" || batches[0].Records != 1 || batches[0].Reason.Details != "400 Bad Request" {
		t.Errorf("batch[0] = %+v", batches[0])
	}
	if batches[1].BatchID != "b-two" || batches[1].Records != 2 {
		t.Errorf("batch[1] = %+v", batches[1])
	}

	if n := rejectedRecords(t, rejectedDir); n != 3 {
		t.Errorf("rejected store holds %d records, want 3", n)
	}
}

func TestListRejectedOnAMissingDirIsEmpty(t *testing.T) {
	batches, err := upload.ListRejected(filepath.Join(t.TempDir(), "never-created"))
	if err != nil || batches != nil {
		t.Errorf("got %v, %v; want empty, nil", batches, err)
	}
}

func openSpool(t *testing.T) *spool.Spool {
	t.Helper()
	sp, err := spool.Create(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

// rejectedRecords sums the record counts ListRejected reports.
func rejectedRecords(t *testing.T, dir string) int {
	t.Helper()
	batches, err := upload.ListRejected(dir)
	if err != nil {
		t.Fatalf("ListRejected: %v", err)
	}
	n := 0
	for _, b := range batches {
		n += b.Records
	}
	return n
}
