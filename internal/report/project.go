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
	// Shape is the form this project records in, as the grant records
	// it: the one answer every surface reads. It is empty when no
	// grant stands.
	Shape routing.Shape

	// InjectedBaseURL is the base URL trajector injected into the
	// project's settings, empty when no base URL is injected;
	// InjectedToken is the consent token that URL carries.
	InjectedBaseURL string
	InjectedToken   string
	// Injected reports an injection of trajector's, in either shape, in
	// the project's settings file; InjectionAgrees reports that it is
	// the injection the grant calls for. The settings file is a file
	// the user also edits, so these two are what it says, never a
	// second answer about the shape.
	Injected        bool
	InjectionAgrees bool
	// HookInstalled reports the ensure-proxy hooks in the project
	// settings; SessionEndInstalled the session-end hook, and
	// GitSnapshotInstalled the one that runs after a shell tool.
	HookInstalled        bool
	SessionEndInstalled  bool
	GitSnapshotInstalled bool

	// AgreementVersion is the accepted data agreement version, empty
	// when none was ever accepted.
	AgreementVersion string
	// ConsentState is this project's recorded decision, empty when the
	// project never decided anything.
	ConsentState consent.ProjectState

	// PauseReason is the device-wide pause, empty while recording.
	PauseReason routing.PauseReason

	// WindowsSideClaude reports that the project lies on a Windows
	// drive mounted into WSL. Claude Code opens such a project from
	// the Windows side, where its hooks run Windows commands that
	// cannot reach this trajector, so nothing is recorded from it.
	WindowsSideClaude bool
}

// Consistent reports the fully healthy enabled state: a standing grant
// with every session hook in place, in the shape the grant records —
// and, in the shape with a base URL, a token exactly what the settings
// inject. status presents it as contributing; doctor treats anything
// else as something to reconcile or report.
func (s ProjectStatus) Consistent() bool {
	return s.InjectionAgrees && s.SessionEndInstalled && s.GitSnapshotInstalled
}

// MissingSessionHooks reports an injection that predates a hook this
// release installs: the rest of it stands, that hook does not. doctor
// completes such an injection in place.
func (s ProjectStatus) MissingSessionHooks() bool {
	return s.Injected && s.HookInstalled && (!s.SessionEndInstalled || !s.GitSnapshotInstalled)
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
