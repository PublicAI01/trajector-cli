package lifecycle

import (
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// ProxyState is who holds the proxy port and, when it is ours, the
// proxy's self-report.
type ProxyState struct {
	Addr   string
	Holder proxylife.Holder
	// Health is meaningful only when Holder is HolderOurs.
	Health proxylife.Health
}

// SpoolState is the capture spool as one readable value.
type SpoolState struct {
	// OpenErr, when non-nil, means the spool could not be opened at
	// all; every other field is then zero.
	OpenErr error
	Usage   int64
	Quota   int64
	// WritableErr is nil while the spool accepts writes within quota.
	WritableErr error
	Days        []spool.DaySummary
}

// Full reports a spool that refuses writes because usage reached the
// quota, the one writability failure with a distinct remedy.
func (s SpoolState) Full() bool { return s.Usage >= s.Quota }

// TokenStoreState is the pairing state with its failure mode kept
// apart: a token store that cannot be read is unknown, not signed out.
type TokenStoreState struct {
	Paired bool
	Err    error
}

// Diagnosis is what the machine knows about this device in one read:
// the current project's consent, the proxy port, the spool, uploads,
// quarantined batches, the service handshake, and the pairing state.
// status renders it, doctor renders and repairs from it, and the
// bundle serializes it — three surfaces, one set of facts.
type Diagnosis struct {
	Project    ProjectStatus
	Proxy      ProxyState
	Spool      SpoolState
	Uploads    upload.State
	Rejected   []upload.RejectedBatch
	Handshake  platform.Handshake
	TokenStore TokenStoreState
	// Selfcheck is the live proxy's own answer for this project's
	// token. It is non-nil only when the project is enabled, our proxy
	// holds the port, and the proxy answered.
	Selfcheck *proxylife.Selfcheck
}

// Diagnose resolves the device's full state. Stores that fail to open
// or read surface inside the value where a surface can present them;
// only the project resolution itself can fail the call.
func (m *Machine) Diagnose(dir string) (Diagnosis, error) {
	var d Diagnosis
	st, err := m.Project(dir)
	if err != nil {
		return d, err
	}
	d.Project = st

	h, holder := m.proxy.Health()
	d.Proxy = ProxyState{Addr: m.proxy.Addr(), Holder: holder, Health: h}
	if st.Enabled && holder == proxylife.HolderOurs {
		if reply, err := m.proxy.Selfcheck(st.Token); err == nil {
			d.Selfcheck = &reply
		}
	}

	if sp, err := m.spool(); err != nil {
		d.Spool = SpoolState{OpenErr: err}
	} else {
		days, err := sp.Summary()
		if err != nil {
			return d, err
		}
		d.Spool = SpoolState{
			Usage:       sp.Usage(),
			Quota:       sp.Quota(),
			WritableErr: sp.Writable(),
			Days:        days,
		}
	}

	d.Uploads = upload.LoadState(m.deps.Layout.UploadDir())
	if d.Rejected, err = upload.ListRejected(m.deps.Layout.RejectedDir()); err != nil {
		return d, err
	}
	d.Handshake = m.handshake()

	_, paired, err := m.deps.Tokens.DeviceToken()
	d.TokenStore = TokenStoreState{Paired: paired, Err: err}
	return d, nil
}
