//go:build !windows

package upload_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakeplatform"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// TestResendPendingReadsOnlyItsOwnBatchsRecords pins the scan
// resendPending performs.
//
// It used Spool.Each until 2026-09-14, which reads the bytes of every
// rawcall on the machine to find the handful a pending lease names — up
// to the whole quota, once a minute, for as long as the lease stands.
// And a lease stands a long time: the failure that leaves one, a plain
// network error on an offline machine, sets no pause at all, so the
// cadence keeps its full minute rate.
//
// The observable edge of that scan is what this test drives. One rawcall
// this machine cannot open is enough to fail the whole walk, and the
// walk ran before the pending batch was resent — so a single unreadable
// file anywhere in the spool blocked the resend of every pending batch
// for good, however healthy that batch's own records were. The
// unreadable record sorts first inside its day directory, so the old
// code met it before it ever reached the batch it was looking for.
func TestResendPendingReadsOnlyItsOwnBatchsRecords(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode, so an unreadable record cannot be staged")
	}
	f := newFixture(t)
	f.server.StubFunc("POST", "/v1/batches", echoAck(t, nil))
	f.storeRawcall(t, "aaa-unrelated", time.Now().UTC())
	f.storeRawcall(t, "req-pending", time.Now().UTC().Add(time.Second))
	writePending(t, f.dir, "b-pinned", spool.Entry{Kind: envelope.KindRawcall, ID: "req-pending"})
	denyRead(t, f.spoolDir, "aaa-unrelated")

	// The flush still stops later, when collect reaches the unreadable
	// record on a pass that does need every record's bytes. What matters
	// here is that the pending batch got out first instead of being held
	// behind a record it never named.
	res, _ := f.uploader.Flush(true)

	if res.Batches != 1 {
		t.Fatalf("batches = %d, want the pending batch resent", res.Batches)
	}
	if n := f.uploadCount(); n != 1 {
		t.Fatalf("service saw %d requests, want 1", n)
	}
	parts, err := fakeplatform.Parts(f.server.Requests()[0])
	if err != nil {
		t.Fatal(err)
	}
	env := string(parts["batch"])
	if !strings.Contains(env, `"b-pinned"`) || !strings.Contains(env, "req-pending") {
		t.Errorf("uploaded envelope = %s, want the pinned id carrying req-pending", env)
	}
	if strings.Contains(env, "aaa-unrelated") {
		t.Errorf("uploaded envelope carried a record outside the pending batch: %s", env)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "pending.json")); !os.IsNotExist(err) {
		t.Errorf("the acknowledged lease was not released: %v", err)
	}
}

// denyRead makes one stored rawcall unopenable, standing in for a record
// this machine cannot read — a permission the user changed, a failing
// disk. The file stays listed, so every reader still meets it.
func denyRead(t *testing.T, spoolDir, id string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(spoolDir, "*", id+".json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("locating the stored rawcall %s: %v (matches: %v)", id, err, matches)
	}
	if err := os.Chmod(matches[0], 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(matches[0], 0o600) })
}
