package lifecycle

import (
	"errors"

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
	// Reason explains a HolderForeign verdict: which way the holder's
	// proof failed. It is what separates an authentication problem from
	// a genuine stranger, so surfaces render it instead of re-deriving
	// a verdict of their own.
	Reason error
}

// SpoolState is the capture spool as one readable value.
type SpoolState struct {
	// OpenErr, when non-nil, means the spool could not be opened or its
	// contents read; every other field is then zero.
	OpenErr error
	Usage   int64
	Quota   int64
	// WritableErr is nil while the spool accepts writes within quota.
	WritableErr error
	Days        []spool.DaySummary
}

// Full reports a spool that refuses writes because usage reached the
// quota, the one writability failure with a distinct remedy. It reads
// the refusal the spool itself returned rather than re-deriving the
// comparison, so a surface can never disagree with the spool about why
// a write would be refused.
func (s SpoolState) Full() bool { return errors.Is(s.WritableErr, spool.ErrQuotaExceeded) }

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
	Project  ProjectStatus
	Proxy    ProxyState
	Spool    SpoolState
	Uploads  upload.State
	Rejected []upload.RejectedBatch
	// RejectedErr, when non-nil, means the quarantined batches could not
	// be read; Rejected is then empty, which must never present as an
	// empty quarantine.
	RejectedErr error
	Handshake   platform.Handshake
	// UpgradeMessage is what the service said when it last refused this
	// client's version, empty once it has acknowledged an upload again.
	// It rides beside the handshake rather than in it because it never
	// came with an acknowledgement.
	UpgradeMessage string
	TokenStore     TokenStoreState
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

	// The verdict is read bare, without proxylife.SettledHealth's
	// startup grace: only callers that act on the verdict pay to wait
	// out a sibling's startup. A diagnosis reports the port as it
	// stands and must answer at once.
	h, holder, why := m.proxy.Health()
	d.Proxy = ProxyState{Addr: m.proxy.Addr(), Holder: holder, Health: h, Reason: why}
	if st.Enabled && holder == proxylife.HolderOurs {
		if reply, err := m.proxy.Selfcheck(st.Token); err == nil {
			d.Selfcheck = &reply
		}
	}

	sp, spoolErr := m.spool()
	var days []spool.DaySummary
	if spoolErr == nil {
		days, spoolErr = sp.Summary()
	}
	if spoolErr != nil {
		d.Spool = SpoolState{OpenErr: spoolErr}
	} else {
		d.Spool = SpoolState{
			Usage:       sp.Usage(),
			Quota:       sp.Quota(),
			WritableErr: sp.Writable(),
			Days:        days,
		}
	}

	d.Uploads = upload.LoadState(m.deps.Layout.UploadDir())
	d.Rejected, d.RejectedErr = upload.ListRejected(m.deps.Layout.RejectedDir())
	d.Handshake = m.handshake()
	d.UpgradeMessage = upload.LoadUpgradeMessage(m.deps.Layout.UploadDir())

	_, paired, err := m.deps.Tokens.DeviceToken()
	d.TokenStore = TokenStoreState{Paired: paired, Err: err}
	return d, nil
}
