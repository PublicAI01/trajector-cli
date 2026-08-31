// Package report is what this device knows about itself as one value,
// and the three ways that value is shown: the status dashboard, the
// findings a doctor run prints, and the JSON a diagnostic bundle
// carries. Resolving the value belongs to the lifecycle machine, and so
// does every repair — nothing here reads a store, writes a file, or
// touches the network, so what any surface says can be settled by
// handing it a value.
package report

import (
	"errors"

	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/proxylife"
	"github.com/PublicAI01/trajector-cli/internal/spool"
	"github.com/PublicAI01/trajector-cli/internal/upload"
)

// SpoolState is the capture spool as one readable value.
type SpoolState struct {
	// Dir is where the spool lives, named in the sentences about it.
	Dir string
	// OpenErr, when non-nil, means the spool could not be opened or its
	// contents read; every other field is then zero.
	OpenErr error
	Usage   int64
	Quota   int64
	// WritableErr is nil while the spool accepts writes within quota.
	WritableErr error
	Days        []spool.DaySummary
}

// full reports a spool that refuses writes because usage reached the
// quota, the one writability failure with a distinct remedy. It reads
// the refusal the spool itself returned rather than re-deriving the
// comparison, so the two surfaces that print a refusal can never
// disagree with the spool about why a write would be refused.
func (s SpoolState) full() bool { return errors.Is(s.WritableErr, spool.ErrQuotaExceeded) }

// TokenStoreState is the pairing state with its failure mode kept
// apart: a token store that cannot be read is unknown, not signed out.
type TokenStoreState struct {
	Paired bool
	Err    error
}

// Diagnosis is what the machine knows about this device in one read:
// this build's identity, the current project's consent, the proxy port,
// the spool, uploads, quarantined batches, the service handshake, and
// the pairing state. status renders it, doctor renders and repairs from
// it, and the bundle serializes it — three surfaces, one set of facts.
type Diagnosis struct {
	// Version is the build that produced this diagnosis, which every
	// surface leads with and the version gates are judged against.
	Version string
	Project ProjectStatus
	// OptionalSettings is each optional Claude Code setting's state for
	// the current project, resolved only while it contributes. status
	// renders it and doctor never does: an optional setting left off is
	// not a fault.
	OptionalSettings []OptionalSettingStatus
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
	// RejectedDir is where quarantined batches wait, named in the
	// sentences about them.
	RejectedDir string
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
