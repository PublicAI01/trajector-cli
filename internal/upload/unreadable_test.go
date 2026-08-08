package upload_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/upload"
)

func TestATornRawcallIsSetAsideAndTheRestUploads(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-good", time.Now().UTC())
	f.storeRawcall(t, "req-torn", time.Now().UTC().Add(time.Second))
	torn := f.tearStoredRawcall(t, "req-torn")

	res, err := f.uploader.Flush(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.Uploaded || res.Batches != 1 || res.Records != 1 || res.Unreadable != 1 {
		t.Fatalf("result = %+v, want one uploaded record and one set aside", res)
	}
	if n := spooledCount(t, f.spool); n != 0 {
		t.Errorf("spool holds %d records; the torn one must move aside, the rest upload", n)
	}
	if f.uploadCount() != 1 {
		t.Fatalf("service saw %d requests, want 1", f.uploadCount())
	}

	batches, err := upload.ListRejected(f.rejected)
	if err != nil || len(batches) != 1 {
		t.Fatalf("rejected store = %+v, %v; want the torn record quarantined", batches, err)
	}
	b := batches[0]
	if b.Records != 1 {
		t.Errorf("quarantined records = %d, want 1", b.Records)
	}
	for _, want := range []string{"never sent", "req-torn"} {
		if !strings.Contains(b.Reason.Details, want) {
			t.Errorf("reason details = %q, want it to contain %q", b.Reason.Details, want)
		}
	}
	kept, err := os.ReadFile(filepath.Join(f.rejected, b.BatchID, "req-torn.json"))
	if err != nil {
		t.Fatalf("quarantined record missing: %v", err)
	}
	if !bytes.Equal(kept, torn) {
		t.Error("the quarantined bytes differ from what was on disk")
	}
	if !strings.Contains(f.logs.String(), "set aside 1 unreadable rawcall(s)") {
		t.Errorf("log = %q, want the set-aside reported", f.logs.String())
	}

	res, err = f.uploader.Flush(true)
	if err != nil || res.Outcome != upload.Empty {
		t.Fatalf("flush after the set-aside = %+v, %v; nothing may linger", res, err)
	}
}

func TestAFlushOfOnlyTornRawcallsSetsThemAsideWithoutUploading(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-torn", time.Now().UTC())
	f.tearStoredRawcall(t, "req-torn")

	res, err := f.uploader.Flush(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.Empty || res.Batches != 0 || res.Unreadable != 1 {
		t.Fatalf("result = %+v, want nothing uploaded and one record set aside", res)
	}
	if f.uploadCount() != 0 {
		t.Errorf("service saw %d requests, want 0", f.uploadCount())
	}
	if _, err := os.Stat(filepath.Join(f.dir, "pending.json")); !os.IsNotExist(err) {
		t.Error("a batch of only set-aside records left its pending record behind")
	}
	if n := rejectedRecords(t, f.rejected); n != 1 {
		t.Errorf("rejected store holds %d records, want 1", n)
	}

	res, err = f.uploader.Flush(true)
	if err != nil || res.Outcome != upload.Empty || res.Unreadable != 0 {
		t.Fatalf("second flush = %+v, %v; the set-aside must not repeat", res, err)
	}
}

func TestAPendingBatchWithATornRecordResendsTheRestUnderTheSameID(t *testing.T) {
	f := newFixture(t)
	f.server.Stub("POST", "/v1/batches", rejectStub(503, "down"))
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", time.Now().UTC())
	f.storeRawcall(t, "req-2", time.Now().UTC().Add(time.Second))

	if _, err := f.uploader.Flush(true); err == nil {
		t.Fatal("first flush against a failing service did not error")
	}
	f.tearStoredRawcall(t, "req-2")

	res, err := f.uploader.Flush(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.Uploaded || res.Batches != 1 || res.Records != 1 || res.Unreadable != 1 {
		t.Fatalf("result = %+v, want the readable record resent and the torn one set aside", res)
	}
	reqs := f.server.Requests()
	if len(reqs) != 2 {
		t.Fatalf("service saw %d requests, want 2", len(reqs))
	}
	first, second := uploadedBatchID(t, reqs[0]), uploadedBatchID(t, reqs[1])
	if first == "" || first != second {
		t.Fatalf("resend batch id = %q, first attempt used %q; they must match", second, first)
	}
	if _, err := os.Stat(filepath.Join(f.rejected, first, "req-2.json")); err != nil {
		t.Errorf("the torn record is not quarantined under its batch id: %v", err)
	}
}

func TestAnUnreadablePendingRecordIsSetAsideAndUploadsResume(t *testing.T) {
	cases := []struct {
		name    string
		pending []byte
	}{
		{"empty file", []byte{}},
		{"torn json", []byte(`{"batch_id":"b-torn`)},
		{"no batch id", []byte(`{"request_ids":["req-1"]}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
			f.storeRawcall(t, "req-1", time.Now().UTC())
			if err := os.MkdirAll(f.dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.dir, "pending.json"), tc.pending, 0o600); err != nil {
				t.Fatal(err)
			}

			res, err := f.uploader.Flush(true)
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != upload.Uploaded || res.Batches != 1 {
				t.Fatalf("result = %+v, want the flush to resume uploading", res)
			}
			aside, err := os.ReadFile(filepath.Join(f.dir, "pending-unreadable.json"))
			if err != nil {
				t.Fatalf("the unreadable pending bytes were not preserved: %v", err)
			}
			if !bytes.Equal(aside, tc.pending) {
				t.Errorf("preserved bytes = %q, want %q", aside, tc.pending)
			}
			if _, err := os.Stat(filepath.Join(f.dir, "pending.json")); !os.IsNotExist(err) {
				t.Error("the unreadable pending record is still in place")
			}
			for _, want := range []string{"warning", "fresh batch id", "stored twice"} {
				if !strings.Contains(f.logs.String(), want) {
					t.Errorf("log = %q, want it to state %q", f.logs.String(), want)
				}
			}
		})
	}
}

func TestAPendingRecordThatCannotBeReadStillStopsTheFlush(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "req-1", time.Now().UTC())
	// A directory at the pending path fails the read itself, which is
	// the unknown case: the record may still be valid, so nothing may
	// discard it.
	if err := os.MkdirAll(filepath.Join(f.dir, "pending.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := f.uploader.Flush(true); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("flush error = %v, want the pending read failure surfaced", err)
	}
	if f.uploadCount() != 0 {
		t.Errorf("service saw %d requests, want 0 while the pending state is unknown", f.uploadCount())
	}
	if f.spool.Usage() == 0 {
		t.Error("spool was drained while the pending state is unknown")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "pending-unreadable.json")); !os.IsNotExist(err) {
		t.Error("a read failure was treated as the unreadable classification")
	}
}
