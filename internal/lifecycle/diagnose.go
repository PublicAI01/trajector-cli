package lifecycle

import (
	"errors"

	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

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
	Project ProjectStatus
	// Proxy is who holds the proxy port, read without the startup grace
	// only callers about to act on it pay.
	Proxy    proxylife.Verdict
	Spool    SpoolState
	Uploads  upload.State
	Rejected []upload.RejectedBatch
	// RejectedErr, when non-nil, means the quarantined batches could not
	// be read; Rejected is then empty, which must never present as an
	// empty quarantine.
	RejectedErr error
	Handshake   platform.Handshake
	// Standings is every reason uploads are held back right now, each
	// carrying its own explanation and remedy. It is a list because more
	// than one can hold at a time — an old build whose account is also
	// unauthorized — and a surface that could name only one of them would
	// send the user to fix half the problem. Empty means uploads flow.
	Standings  []upload.Standing
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
	d.Standings = upload.LoadStandings(m.deps.Layout.UploadDir(), m.deps.Version, m.deps.Now())
	// The quarantine-only standing is derived here and nowhere else:
	// this is the one place that knows both halves of it — that the
	// spool has nothing left to send, and that batches are waiting in
	// quarantine. The sentence it prints still belongs to the standing.
	if d.Spool.OpenErr == nil && d.Spool.Usage == 0 && len(d.Rejected) > 0 {
		d.Standings = append(d.Standings, upload.Standing{Reason: upload.QuarantineOnly})
	}

	_, paired, err := m.deps.Tokens.DeviceToken()
	d.TokenStore = TokenStoreState{Paired: paired, Err: err}
	return d, nil
}
