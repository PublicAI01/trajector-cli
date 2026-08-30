package lifecycle

import (
	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/consent"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/report"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// Diagnose resolves the device's full state, the one value status,
// doctor, and the bundle each render. Stores that fail to open or read
// surface inside the value where a surface can present them; only the
// project resolution itself can fail the call.
func (m *Machine) Diagnose(dir string) (report.Diagnosis, error) {
	d := report.Diagnosis{Version: m.deps.Version}
	st, err := m.Project(dir)
	if err != nil {
		return d, err
	}
	d.Project = st

	// Observe, never Settled: only callers that act on the verdict pay
	// to wait out a sibling's startup. A diagnosis reports the port as
	// it stands and must answer at once.
	d.Proxy = m.proxy.Observe()
	if st.Enabled && d.Proxy.Holder == proxylife.HolderOurs {
		if reply, err := m.proxy.Selfcheck(st.Token); err == nil {
			d.Selfcheck = &reply
		}
	}

	sp, spoolErr := m.spool()
	var days []spool.DaySummary
	if spoolErr == nil {
		days, spoolErr = sp.Summary()
	}
	spoolDir := m.deps.Layout.SpoolDir()
	if spoolErr != nil {
		d.Spool = report.SpoolState{Dir: spoolDir, OpenErr: spoolErr}
	} else {
		d.Spool = report.SpoolState{
			Dir:         spoolDir,
			Usage:       sp.Usage(),
			Quota:       sp.Quota(),
			WritableErr: sp.Writable(),
			Days:        days,
		}
	}

	d.Uploads = upload.LoadState(m.deps.Layout.UploadDir())
	d.RejectedDir = m.deps.Layout.RejectedDir()
	d.Rejected, d.RejectedErr = upload.ListRejected(d.RejectedDir)
	d.Handshake = m.handshake()
	d.Standings = upload.LoadStandings(m.deps.Layout.UploadDir(), m.deps.Version, m.deps.Now())
	// The quarantine-only standing is derived here and nowhere else:
	// this is the one place that knows both halves of it — that the
	// spool has nothing left to send, and that batches are waiting in
	// quarantine. The sentence it prints still belongs to the standing.
	if d.Spool.OpenErr == nil && d.Spool.Usage == 0 && len(d.Rejected) > 0 {
		d.Standings = append(d.Standings, upload.Standing{Reason: upload.QuarantineOnly})
	}

	_, paired, err := m.tokens.DeviceToken()
	d.TokenStore = report.TokenStoreState{Paired: paired, Err: err}
	return d, nil
}

// Project resolves one project's full consent status. A store that
// cannot be read surfaces as the error; the zero fields of a partial
// status are never presented as facts.
func (m *Machine) Project(dir string) (report.ProjectStatus, error) {
	root, err := consent.CanonicalRoot(dir)
	if err != nil {
		return report.ProjectStatus{}, err
	}
	st := report.ProjectStatus{Root: root, Hash: consent.ProjectIDHash(root)}

	grant, enabled, err := m.routes.Active(root)
	if err != nil {
		return st, err
	}
	if enabled {
		st.Enabled = true
		st.Token = grant.Token
		st.Upstream = grant.Upstream
		st.UpstreamMoved = grant.UpstreamMoved
		st.GrantHash = grant.ProjectIDHash
	}
	if st.PauseReason, err = m.routes.PausedReason(); err != nil {
		return st, err
	}

	settings := st.SettingsPath()
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
