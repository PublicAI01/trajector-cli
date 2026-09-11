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
	"time"

	"github.com/PublicAI01/trajector-cli/internal/claudesettings"
	"github.com/PublicAI01/trajector-cli/internal/follow"
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
	// OldestRecord is when the oldest record of either slot still
	// waiting in the spool was captured, zero when none waits. With the
	// day summaries it is how far behind uploading the records are.
	OldestRecord time.Time
}

// recordsWaiting counts by kind the records the spool still holds,
// which is what has not been uploaded yet.
func (s SpoolState) recordsWaiting() (rawcalls, segments, snapshots int) {
	for _, day := range s.Days {
		rawcalls += day.Rawcalls
		segments += day.Segments
		snapshots += day.Snapshots
	}
	return rawcalls, segments, snapshots
}

// SessionFilesState is what the registry of an enabled project's
// session files says, resolved in one read and without opening any
// session file: how many sessions are registered, when one was last
// read, how far reading is behind the files, and what the search for
// the project's earlier files could not cover. It carries counts and
// sizes only — never a session id or a session file's path — because
// status prints it and the bundle serializes it.
type SessionFilesState struct {
	// Err, when non-nil, means the registry could not be read; every
	// other field is then zero.
	Err error
	// Sessions counts the registered sessions: their own files, not
	// the agent files kept beside them.
	Sessions int
	// LastReadAt is the most recent time any registered file was
	// read, zero when none was read yet.
	LastReadAt time.Time
	// BytesBehind is how much of the registered files lies past their
	// cursors: what a reader has yet to consume.
	BytesBehind int64
	// Gaps is what the search for earlier files left uncovered, as
	// recorded with the registry when the search ran.
	Gaps follow.Gaps
	// Signals is what reading the files noticed about their shape, as
	// the registry accumulated it: counts and field names, never a
	// value.
	Signals follow.Signals
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
	// HookPolicy is the static reading, taken fresh for this
	// diagnosis, of whether Claude Code will load the hooks the
	// project injection installs. It is nil for a project that is not
	// enabled, where there are no hooks to read about.
	HookPolicy *claudesettings.HookPolicy
	// SessionFiles is the registry's account of the current project's
	// session files, zero for a project that is not enabled.
	SessionFiles SessionFilesState
	// ProxyIdleBetweenSessions reports that every enabled project on
	// this device records without the proxy, so the resident process
	// living only while a session is open is the healthy state and an
	// absent proxy is nothing to repair.
	ProxyIdleBetweenSessions bool
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
