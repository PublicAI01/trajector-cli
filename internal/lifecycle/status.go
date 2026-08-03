package lifecycle

import (
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
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
	// are what the routing table records for it; GrantHash is carried
	// separately from Hash so a disagreement between the two stores is
	// observable, never papered over.
	Enabled   bool
	Token     string
	Upstream  string
	GrantHash string

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
	PauseReason string
}

// Project resolves one project's full consent status. A store that
// cannot be read surfaces as the error; the zero fields of a partial
// status are never presented as facts.
func (m *Machine) Project(dir string) (ProjectStatus, error) {
	root, err := consent.CanonicalRoot(dir)
	if err != nil {
		return ProjectStatus{}, err
	}
	st := ProjectStatus{Root: root, Hash: consent.ProjectIDHash(root)}

	grant, enabled, err := m.routes.Active(root)
	if err != nil {
		return st, err
	}
	if enabled {
		st.Enabled = true
		st.Token = grant.Token
		st.Upstream = grant.Upstream
		st.GrantHash = grant.ProjectIDHash
	}
	if st.PauseReason, err = m.routes.PausedReason(); err != nil {
		return st, err
	}

	settings := claudesettings.ProjectLocalPath(root)
	if url, ok := claudesettings.InjectedBaseURL(settings); ok {
		st.InjectedBaseURL = url
		st.InjectedToken, _ = claudesettings.TokenFromBaseURL(url)
	}
	st.HookInstalled = claudesettings.HasHook(settings, claudesettings.EnsureProxyMarker)

	if st.AgreementVersion, _, err = m.consent.AcceptedVersion(); err != nil {
		return st, err
	}
	state, ok, err := m.consent.ProjectState(st.Hash)
	if err != nil {
		return st, err
	}
	if ok {
		st.ConsentState = state
	}
	return st, nil
}
