package upload_test

import (
	"fmt"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/platform"
)

func TestFlushAlternatesBackloggedSourcesWithinTheBatchLimit(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", fakeplatform.Acknowledges(platform.Handshake{FlushBytes: 1}))
	f.storeRawcall(t, "prime", f.now)
	if _, err := f.uploader.Flush(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	f.uploader = f.newUploader(t)

	for i := range 3 {
		f.storeRawcall(t, fmt.Sprintf("req-%d", i), f.now)
		f.storeSegment(t, sessionX, i, f.now, "{}\n")
	}
	res, err := f.uploader.Flush(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Batches != 6 || res.Records != 6 {
		t.Fatalf("result = %+v, want six one-record batches", res)
	}
	requests := f.server.Requests()[1:]
	if len(requests) != 6 {
		t.Fatalf("service saw %d batches, want six", len(requests))
	}
	seen := make(map[string]bool)
	for i, request := range requests {
		ix := uploadedIndex(t, request)
		if len(ix.Records) != 1 {
			t.Fatalf("batch %d contains %d records, want one", i, len(ix.Records))
		}
		item := ix.Records[0]
		want := "proxy"
		if i%2 != 0 {
			want = "transcript"
		}
		if item.Source != want {
			t.Errorf("batch %d source = %q, want %q", i, item.Source, want)
		}
		if seen[item.RecordID] {
			t.Errorf("record %q was uploaded more than once", item.RecordID)
		}
		seen[item.RecordID] = true
	}
	if f.spool.Usage() != 0 {
		t.Fatal("acknowledged records remain in the spool")
	}
}
