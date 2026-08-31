package consent_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/consent"
)

func grantProject(t *testing.T, s *consent.Store, hash string) {
	t.Helper()
	if err := s.SetProjectState(hash, "/project/p", consent.StateGranted, "2026-08-31T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

func TestSettingDecisionRoundTripsEachAnswerShape(t *testing.T) {
	cases := []struct {
		name     string
		decision consent.SettingDecision
	}{
		{"declined", consent.SettingDecision{
			Answer:    consent.AnswerDeclined,
			DecidedAt: "2026-08-31T10:01:00Z",
		}},
		{"accepted with nothing written because the value was already true", consent.SettingDecision{
			Answer:    consent.AnswerAccepted,
			Prior:     consent.PriorTrue,
			DecidedAt: "2026-08-31T10:02:00Z",
		}},
		{"accepted and written over an absent key", consent.SettingDecision{
			Answer:    consent.AnswerAccepted,
			Prior:     consent.PriorAbsent,
			DecidedAt: "2026-08-31T10:03:00Z",
		}},
		{"accepted and written over an explicit false", consent.SettingDecision{
			Answer:    consent.AnswerAccepted,
			Prior:     consent.PriorFalse,
			DecidedAt: "2026-08-31T10:04:00Z",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			grantProject(t, s, "hash-p")
			if err := s.SetSettingDecision("hash-p", "showThinkingSummaries", tc.decision); err != nil {
				t.Fatal(err)
			}
			reopened := consent.Open(s.Path())
			decisions, err := reopened.SettingDecisions("hash-p")
			if err != nil {
				t.Fatal(err)
			}
			if got := decisions["showThinkingSummaries"]; got != tc.decision {
				t.Errorf("round-tripped decision = %+v, want %+v", got, tc.decision)
			}
			if len(decisions) != 1 {
				t.Errorf("decisions = %+v, want exactly one", decisions)
			}
		})
	}
}

func TestSettingDecisionRejectsMalformedShapes(t *testing.T) {
	cases := []struct {
		name      string
		hash, key string
		decision  consent.SettingDecision
	}{
		{"accepted without a prior state", "hash-p", "showThinkingSummaries",
			consent.SettingDecision{Answer: consent.AnswerAccepted, DecidedAt: "2026-08-31T10:00:00Z"}},
		{"accepted with an unknown prior state", "hash-p", "showThinkingSummaries",
			consent.SettingDecision{Answer: consent.AnswerAccepted, Prior: "maybe", DecidedAt: "2026-08-31T10:00:00Z"}},
		{"declined carrying a prior state", "hash-p", "showThinkingSummaries",
			consent.SettingDecision{Answer: consent.AnswerDeclined, Prior: consent.PriorAbsent, DecidedAt: "2026-08-31T10:00:00Z"}},
		{"unknown answer", "hash-p", "showThinkingSummaries",
			consent.SettingDecision{Answer: "shrugged", DecidedAt: "2026-08-31T10:00:00Z"}},
		{"empty answer", "hash-p", "showThinkingSummaries",
			consent.SettingDecision{DecidedAt: "2026-08-31T10:00:00Z"}},
		{"empty setting key", "hash-p", "",
			consent.SettingDecision{Answer: consent.AnswerDeclined, DecidedAt: "2026-08-31T10:00:00Z"}},
		{"empty project hash", "", "showThinkingSummaries",
			consent.SettingDecision{Answer: consent.AnswerDeclined, DecidedAt: "2026-08-31T10:00:00Z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			grantProject(t, s, "hash-p")
			if err := s.SetSettingDecision(tc.hash, tc.key, tc.decision); err == nil {
				t.Error("malformed decision was recorded")
			}
			decisions, err := s.SettingDecisions("hash-p")
			if err != nil {
				t.Fatal(err)
			}
			if len(decisions) != 0 {
				t.Errorf("store holds %+v after a rejected decision", decisions)
			}
		})
	}
}

func TestSettingDecisionNeedsARecordedProject(t *testing.T) {
	s := open(t)
	err := s.SetSettingDecision("hash-unknown", "showThinkingSummaries", consent.SettingDecision{
		Answer:    consent.AnswerDeclined,
		DecidedAt: "2026-08-31T10:00:00Z",
	})
	if err == nil {
		t.Error("decision for an unrecorded project was accepted")
	}
	if _, ok, err := s.ProjectState("hash-unknown"); err != nil || ok {
		t.Errorf("a project record appeared: ok = %v, err = %v", ok, err)
	}
}

func TestStoreWrittenBeforeSettingDecisionsStillWorks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "consent-under-test.json")
	old := `{
  "agreement": {"version": "2026-08-07", "accepted_at": "2026-08-07T00:00:00Z"},
  "projects": {
    "hash-old": {"root_path": "/project/old", "state": "granted", "updated_at": "2026-08-07T00:00:01Z"}
  }
}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s := consent.Open(path)

	version, at, err := s.AcceptedVersion()
	if err != nil || version != "2026-08-07" || at != "2026-08-07T00:00:00Z" {
		t.Errorf("AcceptedVersion = %q, %q, %v", version, at, err)
	}
	if state, ok, err := s.ProjectState("hash-old"); err != nil || !ok || state != consent.StateGranted {
		t.Errorf("ProjectState = %q, %v, %v", state, ok, err)
	}
	decisions, err := s.SettingDecisions("hash-old")
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 0 {
		t.Errorf("decisions on an old-format store = %+v, want none", decisions)
	}

	if err := s.SetSettingDecision("hash-old", "showThinkingSummaries", consent.SettingDecision{
		Answer:    consent.AnswerAccepted,
		Prior:     consent.PriorAbsent,
		DecidedAt: "2026-08-31T10:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if state, ok, err := s.ProjectState("hash-old"); err != nil || !ok || state != consent.StateGranted {
		t.Errorf("ProjectState after recording a decision = %q, %v, %v", state, ok, err)
	}
}

func TestClearSettingDecisionLeavesTheRestOfTheProjectAlone(t *testing.T) {
	s := open(t)
	grantProject(t, s, "hash-p")
	written := consent.SettingDecision{Answer: consent.AnswerAccepted, Prior: consent.PriorFalse, DecidedAt: "2026-08-31T10:01:00Z"}
	declined := consent.SettingDecision{Answer: consent.AnswerDeclined, DecidedAt: "2026-08-31T10:02:00Z"}
	if err := s.SetSettingDecision("hash-p", "showThinkingSummaries", written); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSettingDecision("hash-p", "anotherOptionalKey", declined); err != nil {
		t.Fatal(err)
	}

	if err := s.ClearSettingDecision("hash-p", "showThinkingSummaries"); err != nil {
		t.Fatal(err)
	}

	decisions, err := s.SettingDecisions("hash-p")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decisions["showThinkingSummaries"]; ok {
		t.Error("cleared decision is still recorded")
	}
	if got := decisions["anotherOptionalKey"]; got != declined {
		t.Errorf("the other decision changed: %+v", got)
	}
	if state, ok, err := s.ProjectState("hash-p"); err != nil || !ok || state != consent.StateGranted {
		t.Errorf("project state after clear = %q, %v, %v", state, ok, err)
	}
}

func TestClearSettingDecisionIsANoOpWhenNothingIsRecorded(t *testing.T) {
	s := open(t)
	if err := s.ClearSettingDecision("hash-unknown", "showThinkingSummaries"); err != nil {
		t.Errorf("clearing on an unknown project failed: %v", err)
	}
	grantProject(t, s, "hash-p")
	if err := s.ClearSettingDecision("hash-p", "showThinkingSummaries"); err != nil {
		t.Errorf("clearing an unrecorded setting failed: %v", err)
	}
	if state, ok, err := s.ProjectState("hash-p"); err != nil || !ok || state != consent.StateGranted {
		t.Errorf("project state after no-op clear = %q, %v, %v", state, ok, err)
	}
}

func TestProjectStateChangeKeepsSettingDecisions(t *testing.T) {
	s := open(t)
	grantProject(t, s, "hash-p")
	declined := consent.SettingDecision{Answer: consent.AnswerDeclined, DecidedAt: "2026-08-31T10:01:00Z"}
	if err := s.SetSettingDecision("hash-p", "showThinkingSummaries", declined); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectState("hash-p", "/project/p", consent.StateDenied, "2026-08-31T11:00:00Z"); err != nil {
		t.Fatal(err)
	}
	decisions, err := s.SettingDecisions("hash-p")
	if err != nil {
		t.Fatal(err)
	}
	if got := decisions["showThinkingSummaries"]; got != declined {
		t.Errorf("decision after a state change = %+v, want %+v", got, declined)
	}
}

func TestRestoreProjectRollsBackSettingDecisionsRecordedMeanwhile(t *testing.T) {
	s := open(t)
	grantProject(t, s, "hash-p")
	snap, err := s.SnapshotProject("hash-p")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSettingDecision("hash-p", "showThinkingSummaries", consent.SettingDecision{
		Answer:    consent.AnswerAccepted,
		Prior:     consent.PriorAbsent,
		DecidedAt: "2026-08-31T10:01:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreProject(snap); err != nil {
		t.Fatal(err)
	}
	decisions, err := s.SettingDecisions("hash-p")
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 0 {
		t.Errorf("decisions survived the rollback: %+v", decisions)
	}
	if state, ok, err := s.ProjectState("hash-p"); err != nil || !ok || state != consent.StateGranted {
		t.Errorf("project state after rollback = %q, %v, %v", state, ok, err)
	}
}

func TestAcceptanceOfAnEarlierAgreementVersionIsStale(t *testing.T) {
	s := open(t)
	if err := s.AcceptAgreement("2026-08-07", "2026-08-07T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	version, _, err := s.AcceptedVersion()
	if err != nil {
		t.Fatal(err)
	}
	if version == consent.AgreementVersion {
		t.Error("an earlier acceptance matches the current agreement version")
	}
}
