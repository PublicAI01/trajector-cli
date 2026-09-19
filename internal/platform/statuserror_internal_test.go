package platform

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestStatusErrorDisarmsTheServiceReasonPhrase pins the 2026-09-18 fix.
// The status line's reason phrase is free text the service chose, and
// Go's response reader keeps whatever followed the status code, control
// bytes included. Every error built from a StatusError carries it, and
// those messages are printed by `trajector upload`, kept as
// State.LastError for the status dashboard, written into a quarantined
// batch's reason.json, and copied into the diagnostic bundle. Printed
// raw, an escape sequence there repaints the lines around it and forges
// output the user reads as this client's own report — the exact attack
// SafeServiceText disarms for the service's other free text.
func TestStatusErrorDisarmsTheServiceReasonPhrase(t *testing.T) {
	t.Parallel()
	const forged = "400 \x1b[2J\x1b[H trajector: Signed in.\r\nContributing."
	resp := &http.Response{StatusCode: http.StatusBadRequest, Status: forged}

	err := newStatusError(resp, http.MethodPost, BatchesPath, nil)
	if strings.ContainsAny(err.Status, "\x1b\r\n") {
		t.Errorf("StatusError.Status = %q; it still carries terminal control bytes", err.Status)
	}
	if strings.ContainsAny(err.Error(), "\x1b\r\n") {
		t.Errorf("StatusError.Error() = %q; it still carries terminal control bytes", err.Error())
	}

	// The rejection path copies the phrase into a field the uploader
	// writes to disk and prints, so it has to be disarmed there too.
	classified := classifyUploadFailure(resp, nil, 0, 0)
	var rejected *BatchRejectedError
	if !errors.As(classified, &rejected) {
		t.Fatalf("classifyUploadFailure returned %T, want *BatchRejectedError", classified)
	}
	if strings.ContainsAny(rejected.Status, "\x1b\r\n") {
		t.Errorf("BatchRejectedError.Status = %q; it still carries terminal control bytes", rejected.Status)
	}
	if strings.ContainsAny(rejected.Error(), "\x1b\r\n") {
		t.Errorf("BatchRejectedError.Error() = %q; it still carries terminal control bytes", rejected.Error())
	}
}
