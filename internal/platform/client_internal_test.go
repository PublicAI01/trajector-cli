package platform

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestUploadBudgetIsTheBindingConstraint pins the 2026-09-16 fix.
// UploadBatch bounds one attempt by copying the service client and
// setting http.Client.Timeout to the budget UploadBudget returned. The
// copy is shallow, so it shares this transport, and a header timeout
// narrower than the widest budget silently overrides the escalation:
// every attempt is cut off at the same fixed point, each abort reads
// back as a timeout, the budget doubles, and the next attempt repeats
// the one that just failed, with the batch pinned at the head of the
// queue until the spool fills and recording stops. Asserted against
// UploadBudget's own output, so widening the budget cannot quietly
// reintroduce the mismatch.
func TestUploadBudgetIsTheBindingConstraint(t *testing.T) {
	t.Parallel()
	// A batch large enough, after enough consecutive timeouts, to
	// saturate the budget at its cap.
	widest := UploadBudget(64<<20, 16)
	if widest <= requestTimeout {
		t.Fatalf("UploadBudget saturates at %s, which is no wider than requestTimeout %s: this test cannot detect the bug it exists for", widest, requestTimeout)
	}

	agent, ok := newClient("test").Transport.(userAgentTransport)
	if !ok {
		t.Fatalf("service client transport is %T, want userAgentTransport", newClient("test").Transport)
	}
	transport, ok := agent.next.(*http.Transport)
	if !ok {
		t.Fatalf("inner transport is %T, want *http.Transport", agent.next)
	}
	if got := transport.ResponseHeaderTimeout; got != 0 && got < widest {
		t.Errorf("ResponseHeaderTimeout is %s but an upload attempt may be allowed %s: "+
			"the transport cuts the attempt off before its budget is spent, so every retry repeats the attempt that just failed", got, widest)
	}
}

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
