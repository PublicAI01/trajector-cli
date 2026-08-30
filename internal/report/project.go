package report

import (
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

// ProjectStatus is everything the machine knows about one project's
// consent, resolved in one read. It is the machine's read half: the
// write methods change this state, a doctor surface prints it, and
// tests assert on it instead of re-deriving it from the underlying
// stores.
type ProjectStatus struct {
	// Root is the canonical project root; Hash identifies the project
	// in stored records, grants, and consent entries.
	Root string
	Hash string

	// Enabled reports a standing grant. Token, Upstream, and GrantHash
	// are what the routing table records for it; UpstreamMoved carries
	// the last unattended upstream change, zero when the upstream still
	// is what enable granted.
	Enabled       bool
	Token         string
	Upstream      string
	UpstreamMoved routing.UpstreamMove
	GrantHash     string

	// InjectedBaseURL is the base URL trajector injected into the
	// project's settings, empty when nothing is injected;
	// InjectedToken is the consent token that URL carries.
	InjectedBaseURL string
	InjectedToken   string
	// HookInstalled reports the ensure-proxy hooks in the project
	// settings.
	HookInstalled bool

	// AgreementVersion is the accepted data agreement version, empty
	// when none was ever accepted.
	AgreementVersion string
	// ConsentState is this project's recorded decision, empty when the
	// project never decided anything.
	ConsentState consent.ProjectState

	// PauseReason is the device-wide pause, empty while recording.
	PauseReason routing.PauseReason
}

// Injected reports whether trajector's base URL is currently injected
// into the project's settings.
func (s ProjectStatus) Injected() bool { return s.InjectedBaseURL != "" }

// Consistent reports the fully healthy enabled state: a standing grant
// whose token is exactly what the settings inject, with the session
// hooks in place. status presents it as contributing; doctor treats
// anything else as something to reconcile or report.
func (s ProjectStatus) Consistent() bool {
	return s.Enabled && s.InjectedToken == s.Token && s.HookInstalled
}

// IdentityDisagreement reports that the routing table and the consent
// record name different identities for this root. GrantHash is carried
// separately from Hash exactly so this is observable, never papered
// over: doctor reports it, and nothing repairs it by guessing which
// store is right.
func (s ProjectStatus) IdentityDisagreement() bool {
	return s.Enabled && s.GrantHash != s.Hash
}

// SettingsPath is the project-local Claude settings file trajector
// injects into.
func (s ProjectStatus) SettingsPath() string {
	return claudesettings.ProjectLocalPath(s.Root)
}
