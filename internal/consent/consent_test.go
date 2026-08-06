package consent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/consent"
)

func open(t *testing.T) *consent.Store {
	t.Helper()
	return consent.Open(filepath.Join(t.TempDir(), "consent-under-test.json"))
}

func TestFreshStoreHasNoAcceptanceAndNoProjects(t *testing.T) {
	s := open(t)
	version, at, err := s.AcceptedVersion()
	if err != nil || version != "" || at != "" {
		t.Errorf("AcceptedVersion = %q, %q, %v; want empty", version, at, err)
	}
	if _, ok, err := s.ProjectState("hash"); err != nil || ok {
		t.Errorf("ProjectState on fresh store: ok = %v, err = %v", ok, err)
	}
}

func TestAcceptAgreementRoundTrips(t *testing.T) {
	s := open(t)
	if err := s.AcceptAgreement(consent.AgreementVersion, "2026-08-02T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	version, at, err := s.AcceptedVersion()
	if err != nil {
		t.Fatal(err)
	}
	if version != consent.AgreementVersion || at != "2026-08-02T10:00:00Z" {
		t.Errorf("AcceptedVersion = %q, %q", version, at)
	}
}

func TestAcceptAgreementRejectsEmptyVersion(t *testing.T) {
	if err := open(t).AcceptAgreement("", "2026-08-02T10:00:00Z"); err == nil {
		t.Error("empty version accepted")
	}
}

func TestProjectStateTransitions(t *testing.T) {
	s := open(t)
	if err := s.SetProjectState("hash-1", "/home/dev/p", consent.StateGranted, "2026-08-02T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	state, ok, err := s.ProjectState("hash-1")
	if err != nil || !ok || state != consent.StateGranted {
		t.Errorf("after grant: %q, %v, %v", state, ok, err)
	}
	if err := s.SetProjectState("hash-1", "/home/dev/p", consent.StateDenied, "2026-08-02T11:00:00Z"); err != nil {
		t.Fatal(err)
	}
	state, _, _ = s.ProjectState("hash-1")
	if state != consent.StateDenied {
		t.Errorf("after deny: %q", state)
	}
}

func TestStoreFileIsOwnerOnly(t *testing.T) {
	s := open(t)
	if err := s.AcceptAgreement(consent.AgreementVersion, "2026-08-02T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("consent file mode = %v, want owner-only", perm)
	}
}

func TestMalformedStoreSurfacesError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "consent-under-test.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := consent.Open(path)
	if _, _, err := s.AcceptedVersion(); err == nil {
		t.Error("malformed store read did not fail")
	}
	if err := s.AcceptAgreement(consent.AgreementVersion, "2026-08-02T10:00:00Z"); err == nil {
		t.Error("write over a malformed store did not fail")
	}
}

func TestProjectIDHashIsStableAcrossPathSpellings(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "project")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	direct, err := consent.CanonicalRoot(nested)
	if err != nil {
		t.Fatal(err)
	}
	dotted, err := consent.CanonicalRoot(filepath.Join(dir, ".", "project"))
	if err != nil {
		t.Fatal(err)
	}
	if direct != dotted {
		t.Errorf("canonical roots differ: %q vs %q", direct, dotted)
	}
	if consent.ProjectIDHash(direct) != consent.ProjectIDHash(dotted) {
		t.Error("hashes differ for the same project")
	}
	if h := consent.ProjectIDHash(direct); len(h) != 64 || strings.ContainsAny(h, "/\\") {
		t.Errorf("hash %q is not a plain hex digest", h)
	}
}

func TestAgreementTextNamesItsVersion(t *testing.T) {
	if !strings.Contains(consent.AgreementText, consent.AgreementVersion) {
		t.Error("agreement text does not carry its version")
	}
}

func TestRestoreProjectLeavesOtherDecisionsAlone(t *testing.T) {
	s := open(t)
	if err := s.AcceptAgreement(consent.AgreementVersion, "2026-08-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	snap, err := s.SnapshotProject("hash-ours")
	if err != nil {
		t.Fatal(err)
	}
	// A concurrent enable records another project while ours is underway.
	if err := s.SetProjectState("hash-other", "/project/other", consent.StateGranted, "2026-08-01T00:00:01Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectState("hash-ours", "/project/ours", consent.StateGranted, "2026-08-01T00:00:02Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreProject(snap); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := s.ProjectState("hash-ours"); err != nil || ok {
		t.Errorf("our record survived its own rollback: %v, %v", ok, err)
	}
	if state, ok, err := s.ProjectState("hash-other"); err != nil || !ok || state != consent.StateGranted {
		t.Errorf("the concurrent record did not survive our rollback: %v, %v, %v", state, ok, err)
	}
	if version, _, err := s.AcceptedVersion(); err != nil || version != consent.AgreementVersion {
		t.Errorf("agreement acceptance did not survive rollback: %q, %v", version, err)
	}
}

func TestRestoreProjectPutsAPriorDecisionBack(t *testing.T) {
	s := open(t)
	if err := s.SetProjectState("hash-p", "/project/p", consent.StateDenied, "2026-08-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	snap, err := s.SnapshotProject("hash-p")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectState("hash-p", "/project/p", consent.StateGranted, "2026-08-01T00:00:01Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreProject(snap); err != nil {
		t.Fatal(err)
	}
	if state, ok, err := s.ProjectState("hash-p"); err != nil || !ok || state != consent.StateDenied {
		t.Errorf("restored decision = %v, %v, %v; want the prior denied state", state, ok, err)
	}
}
