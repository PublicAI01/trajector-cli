package upload_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// TestLoadStateDisarmsWhatTheServiceSaid pins the read-back half of the
// 2026-09-18 fix. The uploader's state file keeps the last failure's
// message and the last rejection's details, and the status dashboard
// prints both beside its own words. Both can carry text the service
// chose — the status line's reason phrase rides inside every error built
// from a StatusError — so a file written by a build that predates the
// cleaning at the network edge, or edited by hand, must not be able to
// draw its own line on the terminal. It is the same re-cleaning
// LoadHandshake, LoadStandings and readReason already do for their own
// service-supplied fields; this file was the one that did not.
func TestLoadStateDisarmsWhatTheServiceSaid(t *testing.T) {
	dir := t.TempDir()
	const stateFile = `{
  "last_attempt_at": "2026-08-01T00:00:00Z",
  "last_error": "upload: batch b-1: the service rejected the batch: 400 \u001b[2J\u001b[H trajector: everything checks out.",
  "last_error_at": "2026-08-01T00:00:00Z",
  "last_rejected": {
    "batch_id": "b-1",
    "records": 3,
    "cause": "service_refused",
    "details": "400 \u001b[2Jnothing is wrong here",
    "at": "2026-08-01T00:00:00Z"
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(stateFile), 0o600); err != nil {
		t.Fatal(err)
	}

	st := upload.LoadState(dir)
	if strings.ContainsAny(st.LastError, "\x1b\r\n") {
		t.Errorf("LastError = %q; it still carries terminal control bytes", st.LastError)
	}
	if st.LastRejected == nil {
		t.Fatal("LastRejected was dropped; the cleaning must not cost the record")
	}
	if strings.ContainsAny(st.LastRejected.Details, "\x1b\r\n") {
		t.Errorf("LastRejected.Details = %q; it still carries terminal control bytes", st.LastRejected.Details)
	}
	// Cleaning disarms, it does not discard: what the service said must
	// still be readable, or the user loses the only account of why a
	// batch was refused.
	if !strings.Contains(st.LastRejected.Details, "nothing is wrong here") {
		t.Errorf("LastRejected.Details = %q; the service's words were lost, not just disarmed", st.LastRejected.Details)
	}
	if !strings.Contains(st.LastError, "upload: batch b-1") {
		t.Errorf("LastError = %q; the message was lost, not just disarmed", st.LastError)
	}
}
