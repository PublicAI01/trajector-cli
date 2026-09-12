package proxytest

import (
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/consent"
)

// Decision is a project's recorded consent decision, in consent's own
// type, with the two decisions a project can hold. Tests name them
// through the harness so nothing above the proxy has to reach into the
// consent store's vocabulary.
type Decision = consent.ProjectState

const (
	Granted = consent.StateGranted
	Denied  = consent.StateDenied
)

// SettingAnswer is the user's answer to an optional setting, in
// consent's own type, with the two answers a setting can hold.
type SettingAnswer = consent.SettingAnswer

const (
	AnswerAccepted = consent.AnswerAccepted
	AnswerDeclined = consent.AnswerDeclined
)

const (
	PriorAbsent = consent.PriorAbsent
	PriorTrue   = consent.PriorTrue
)

// SettingDecision is one optional setting's recorded answer, in
// consent's own type.
type SettingDecision = consent.SettingDecision

// AgreementVersion and AgreementText are the terms this build asks
// for, read from the one module that states them.
const (
	AgreementVersion = consent.AgreementVersion
	AgreementText    = consent.AgreementText
)

// CanonicalRoot normalizes a project directory the way every derivation
// of a project identity normalizes it.
func CanonicalRoot(t *testing.T, dir string) string {
	t.Helper()
	root, err := consent.CanonicalRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// ProjectIDHash is a canonical root's identifier in stored records.
func ProjectIDHash(canonicalRoot string) string { return consent.ProjectIDHash(canonicalRoot) }

func (s *Sandbox) consents() *consent.Store { return consent.Open(s.layout.ConsentFile()) }

// AcceptAgreement records acceptance of an agreement version, as
// answering the enable prompt would. A version other than
// AgreementVersion leaves the device holding stale terms.
func (s *Sandbox) AcceptAgreement(version, at string) {
	s.t.Helper()
	if err := s.consents().AcceptAgreement(version, at); err != nil {
		s.t.Fatal(err)
	}
}

// AcceptedAgreement reports the agreement version this device accepted
// and when; both are empty when it accepted none.
func (s *Sandbox) AcceptedAgreement() (version, acceptedAt string) {
	s.t.Helper()
	version, acceptedAt, err := s.consents().AcceptedVersion()
	if err != nil {
		s.t.Fatal(err)
	}
	return version, acceptedAt
}

// DecideProject records a project's decision, as a finished enable or
// disable would.
func (s *Sandbox) DecideProject(projectIDHash, root string, d Decision, at string) {
	s.t.Helper()
	if err := s.consents().SetProjectState(projectIDHash, root, d, at); err != nil {
		s.t.Fatal(err)
	}
}

// DecideSetting records the answer for one optional setting of a
// project that already holds a decision.
func (s *Sandbox) DecideSetting(projectIDHash, settingKey string, d SettingDecision) {
	s.t.Helper()
	if err := s.consents().SetSettingDecision(projectIDHash, settingKey, d); err != nil {
		s.t.Fatal(err)
	}
}

// SettingDecisions reports a project's recorded setting answers by
// setting key; a project with none yields an empty map.
func (s *Sandbox) SettingDecisions(projectIDHash string) map[string]SettingDecision {
	s.t.Helper()
	decisions, err := s.consents().SettingDecisions(projectIDHash)
	if err != nil {
		s.t.Fatal(err)
	}
	return decisions
}
