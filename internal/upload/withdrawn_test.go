package upload_test

import (
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// TestFlushNeverSendsRecordsOfAProjectThatWithdrewConsent pins the last
// door out of the machine. Withdrawal is enforced in two processes that
// cannot order themselves against each other: the proxy re-reads the
// routing table when it writes a capture, through a cache with a
// lifetime of its own, while `disable` deletes that project's spooled
// records from a separate short-lived process. A streamed exchange whose
// response closes just after the revoke lands is written on a stale
// verdict, and if disable's scan has already swept the day directory the
// record stays. Nothing looked at it again — the uploader did not
// consult consent — so it shipped: data captured after the user
// withdrew, which is what `disable` tells them cannot happen.
func TestFlushNeverSendsRecordsOfAProjectThatWithdrewConsent(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcallFor(t, "req-still-granted", "hash-granted", time.Now().UTC())
	// What the race leaves behind: captured for a project whose consent
	// has since been withdrawn, and missed by disable's purge.
	f.storeRawcallFor(t, "req-after-withdrawal", "hash-withdrawn", time.Now().UTC())
	f.withdrawn["hash-withdrawn"] = true

	res, err := f.uploader.Flush(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != upload.Uploaded || res.Records != 1 {
		t.Fatalf("result = %+v, want one acknowledged record — only the project still granting", res)
	}
	for _, r := range f.server.Requests() {
		parts, err := fakeplatform.Parts(r)
		if err != nil {
			t.Fatalf("reading upload request: %v", err)
		}
		if strings.Contains(string(parts["batch"]), "req-after-withdrawal") {
			t.Fatalf("a batch carried a record whose project had withdrawn consent:\n%s", parts["batch"])
		}
	}
	if usage := f.spool.Usage(); usage != 0 {
		t.Errorf("spool holds %d bytes; the withdrawn record was neither sent nor deleted", usage)
	}
	if !strings.Contains(f.logs.String(), "withdrawn consent") {
		t.Errorf("deleting a withdrawn project's record went unreported:\n%s", f.logs.String())
	}
}

// TestFlushWithEveryRecordWithdrawnReleasesTheBatch covers the whole
// batch going: the pending record it pinned must be released, or every
// later flush would resend an id with nothing behind it.
func TestFlushWithEveryRecordWithdrawnReleasesTheBatch(t *testing.T) {
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcallFor(t, "req-gone", "hash-withdrawn", time.Now().UTC())
	f.withdrawn["hash-withdrawn"] = true

	res, err := f.uploader.Flush(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Batches != 0 || len(f.server.Requests()) != 0 {
		t.Errorf("result = %+v with %d request(s), want nothing uploaded", res, len(f.server.Requests()))
	}
	// A second flush must find nothing left rather than a pending batch
	// it can never satisfy.
	again, err := f.uploader.Flush(true)
	if err != nil {
		t.Fatal(err)
	}
	if again.Outcome != upload.Empty {
		t.Errorf("second flush = %+v, want %q", again, upload.Empty)
	}
}
