package report_test

import (
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

func TestAPendingReconfirmReadsAsTheStoredReconfirmPause(t *testing.T) {
	stored, pending := device(), device()
	stored.Project, pending.Project = contributing(), contributing()
	stored.Project.PauseReason = routing.PauseConsentReconfirm
	pending.Project.ReconfirmPending = true

	if got, want := dashboard(pending), dashboard(stored); got != want {
		t.Errorf("status with the reconfirm pending:\n%s\nwant it as with the pause stored:\n%s", got, want)
	}
	gotProblems, got := doctorText(pending)
	wantProblems, want := doctorText(stored)
	if got != want || gotProblems != wantProblems {
		t.Errorf("doctor with the reconfirm pending (%d problem(s)):\n%s\nwant it as with the pause stored (%d):\n%s",
			gotProblems, got, wantProblems, want)
	}
}

func TestAPendingReconfirmAddsNothingToAPauseThatStands(t *testing.T) {
	stored, both := device(), device()
	stored.Project.PauseReason = routing.PauseSignedOut
	both.Project.PauseReason = routing.PauseSignedOut
	both.Project.ReconfirmPending = true

	if got, want := dashboard(both), dashboard(stored); got != want {
		t.Errorf("status with a reconfirm pending under another pause:\n%s\nwant only that pause:\n%s", got, want)
	}
	_, got := doctorText(both)
	_, want := doctorText(stored)
	if got != want {
		t.Errorf("doctor with a reconfirm pending under another pause:\n%s\nwant only that pause:\n%s", got, want)
	}
}

func TestTheBundleKeepsTheStoredPauseBesideAPendingReconfirm(t *testing.T) {
	d := device()
	d.Project.ReconfirmPending = true

	got := string(report.DiagnosisJSON(d))
	wants(t, "diagnosis.json", got, `"pause_reason": ""`, `"reconfirm_pending": true`)
	rejects(t, "diagnosis.json", got, `"pause_explanation"`)
}
