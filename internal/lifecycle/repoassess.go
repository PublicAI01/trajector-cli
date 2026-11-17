package lifecycle

import (
	"context"
	"fmt"
	"io"

	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/redact"
)

// RepoTerms reports the version of the repository assessment terms the
// user accepted on this device and when; both are empty when none was.
func (m *Machine) RepoTerms() (version, acceptedAt string, err error) {
	version, acceptedAt, err = m.consent.AcceptedRepoTerms()
	if err != nil {
		return "", "", fmt.Errorf("reading the consent record at %s: %w", m.consent.Path(), err)
	}
	return version, acceptedAt, nil
}

// AcceptRepoTerms records that the user accepted the repository
// assessment terms this build shows, now.
func (m *Machine) AcceptRepoTerms() error {
	return m.consent.AcceptRepoTerms(consent.RepoTermsVersion, m.now())
}

// WarnNonDefaultEndpoint names the destination on w when it is not the
// default trajector service. A surface that sends data says so before
// the user confirms the sending, so the override is never silent.
func (m *Machine) WarnNonDefaultEndpoint(w io.Writer) { m.warnNonDefaultEndpoint(w) }

// SubmitRepoAssessment sends one repository assessment with this
// device's credential and returns the id the service stored it under.
// It is sent once: a failure is returned to be reported, and nothing
// about it is kept.
func (m *Machine) SubmitRepoAssessment(ctx context.Context, body redact.RedactedBytes) (string, error) {
	if err := m.CanSubmitRepoAssessment(); err != nil {
		return "", err
	}
	token, _ := m.deviceToken()
	return m.service.SubmitRepoAssessment(ctx, token, body)
}

// CanSubmitRepoAssessment reports a device that cannot send a
// repository assessment because it is not signed in. A command asks it
// first, before it asks the user anything.
func (m *Machine) CanSubmitRepoAssessment() error {
	if !m.Paired() {
		return fmt.Errorf("%w: run `trajector login`, then run this command again; nothing was sent", errNotPaired)
	}
	return nil
}
