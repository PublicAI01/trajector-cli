package report

import (
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/routing"
)

// OptionalSettingStatus is one optional Claude Code setting as status
// presents it: its key, its classified state, and whether the user
// declined it. Declined rides beside the state because a declined
// setting is still off — the two together decide whether status
// recommends or merely records.
type OptionalSettingStatus struct {
	Key      string
	State    claudesettings.SettingState
	Declined bool
}

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
	// GrantNoProxy is the shape the grant records: true when enable
	// installed hooks without a base URL. The grant is the record of
	// what the user chose; NoProxy below is what the settings file
	// carries now.
	GrantNoProxy bool

	// InjectedBaseURL is the base URL trajector injected into the
	// project's settings, empty when no base URL is injected;
	// InjectedToken is the consent token that URL carries.
	InjectedBaseURL string
	InjectedToken   string
	// NoProxy reports the injection shape that carries no base URL:
	// the session hooks stand, marked as such, and the project's
	// traffic is not routed through the proxy.
	NoProxy bool
	// HookInstalled reports the ensure-proxy hooks in the project
	// settings; SessionEndInstalled the session-end hook.
	HookInstalled       bool
	SessionEndInstalled bool

	// AgreementVersion is the accepted data agreement version, empty
	// when none was ever accepted.
	AgreementVersion string
	// ConsentState is this project's recorded decision, empty when the
	// project never decided anything.
	ConsentState consent.ProjectState

	// PauseReason is the device-wide pause, empty while recording.
	PauseReason routing.PauseReason
}

// Injected reports whether an injection of trajector's, in either
// shape, currently stands in the project's settings.
func (s ProjectStatus) Injected() bool { return s.InjectedBaseURL != "" || s.NoProxy }

// Consistent reports the fully healthy enabled state: a standing grant
// with all three session hooks in place, in the shape the grant
// records — and, in the shape with a base URL, a token exactly what
// the settings inject. status presents it as contributing; doctor
// treats anything else as something to reconcile or report.
func (s ProjectStatus) Consistent() bool {
	if !s.Enabled || !s.HookInstalled || !s.SessionEndInstalled {
		return false
	}
	if s.GrantNoProxy {
		return s.NoProxy
	}
	return !s.NoProxy && s.InjectedToken == s.Token
}

// MissingSessionEnd reports an injection that predates the session-end
// hook: the rest of it stands, that hook does not. doctor completes
// such an injection in place.
func (s ProjectStatus) MissingSessionEnd() bool {
	return s.Injected() && s.HookInstalled && !s.SessionEndInstalled
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
