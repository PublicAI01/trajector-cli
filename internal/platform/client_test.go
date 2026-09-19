package platform_test

import (
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/repotest"
)

// An upload attempt is bounded by the budget its caller was given, and
// by nothing narrower: the service client is copied per attempt and the
// copy shares one transport, so a header timeout set on that transport
// cuts every attempt off at the same fixed point however far the budget
// has escalated. Each abort then reads back as a timeout, the budget
// doubles, and the next attempt repeats the one that just failed, with
// the batch pinned at the head of the queue until the spool fills and
// recording stops. This package's transport therefore derives its
// header timeout from the widest budget an attempt may be given, and
// never spells a duration of its own. Other packages bound their own
// clients as their own exchanges require; what cannot happen is a
// second, narrower bound inside the client the uploader rides.
func TestResponseHeaderTimeoutIsDerivedFromTheWidestUploadBudget(t *testing.T) {
	t.Parallel()
	// The field name is assembled at run time so this test is not a finding.
	field := "Response" + "HeaderTimeout"

	var assignments []string
	repotest.Lines(t, func(l repotest.Line) {
		text := strings.TrimSpace(l.Text)
		if l.File.Test() || !strings.HasPrefix(l.File.Rel, "internal/platform/") ||
			strings.HasPrefix(text, "//") || !strings.Contains(text, field) {
			return
		}
		assignments = append(assignments, l.String())
	})
	if len(assignments) != 1 {
		t.Fatalf("this package sets the header timeout in %d places, want exactly one:\n%s", len(assignments), strings.Join(assignments, "\n"))
	}
	if !strings.HasSuffix(assignments[0], field+" = maxUploadBudget") {
		t.Errorf("%s\nthe header timeout must be the widest budget an attempt may be given, not a duration of its own", assignments[0])
	}
}
