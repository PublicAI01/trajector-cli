package platform

import (
	"net/http"
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
