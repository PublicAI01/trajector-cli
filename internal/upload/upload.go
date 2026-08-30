// Package upload drains the spool into acknowledged batches. A flush
// triggers when the spool crosses a size or age threshold (the service
// can tune both through the handshake it returns with every
// acknowledgement) and then drains the spool completely, one bounded
// batch at a time. Records are deleted only after the service
// acknowledges the batch that carried them, and a batch id is assigned
// before its first attempt and persisted until acknowledgement, so a
// retried upload can never be ingested twice.
package upload

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/batch"
	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// Default flush thresholds, used until a handshake overrides them.
const (
	DefaultFlushBytes int64 = 10 << 20
	DefaultFlushAge         = 24 * time.Hour
)

// Disposition is what this client does with one batch once the service
// has answered for it. The five values are the whole of the answer set
// the upload contract defines, and the service side names the same
// five, so an answer that maps to none of them is a contract this
// client does not implement rather than a batch it mishandled.
//
// A disposition is about one batch. What a whole flush amounts to is an
// Outcome, derived from the disposition of the batch the flush stopped
// at plus facts only the flush knows — how many batches it acknowledged,
// whether the spool was empty, whether it stayed under the thresholds.
type Disposition string

const (
	// Ack: the service acknowledged the batch, so its records are
	// deleted. The only value that deletes anything.
	Ack Disposition = "ack"
	// RetrySameID: keep the data and offer it again under the same batch
	// id. Nothing is deleted and nothing is quarantined, so a service
	// that already stored the batch recognizes the id and does not store
	// it twice.
	RetrySameID Disposition = "retry_same_id"
	// PauseUploads: the service refuses this client version. Everything
	// is kept, nothing is quarantined, and automatic flushes stop until
	// an upload is acknowledged.
	PauseUploads Disposition = "pause_uploads"
	// PauseUploadsAuthorize: what PauseUploads does, with the user told
	// to complete their data authorization instead of to upgrade. It is
	// a value of its own because what the user reads is the whole of
	// what this answer exists to get right.
	PauseUploadsAuthorize Disposition = "pause_uploads_authorize"
	// Quarantine: the service will never take this batch. Its records
	// move to the local quarantine — never deleted — so the uploads
	// behind them flow again.
	Quarantine Disposition = "quarantine"
)

// Outcome names the terminal states of one Flush call: what the
// disposition of the batch the flush stopped at means for the flush as
// a whole, or, when no batch was offered, what stopped it from
// offering one.
type Outcome string

const (
	// Uploaded means at least one batch was acknowledged.
	Uploaded Outcome = "uploaded"
	// Empty means the spool held nothing.
	Empty Outcome = "empty"
	// BelowThreshold means an unforced flush found the spool under both
	// thresholds.
	BelowThreshold Outcome = "below_threshold"
	// Paused means no device token is stored; nothing was attempted.
	// Capture continues — pairing again resumes uploads.
	Paused Outcome = "paused"
	// UpgradeRequired means the service refused this client version;
	// automatic flushes stop for this process. Data is untouched, and a
	// forced flush may still try.
	UpgradeRequired Outcome = "upgrade_required"
	// AuthorizationRequired means the service will not take uploads from
	// this account until its data authorization is completed on the web;
	// automatic flushes stop for this process. Data is untouched, and a
	// forced flush may still try — which is the recovery path, since the
	// user completes the authorization elsewhere and nothing local has to
	// change for uploads to resume.
	AuthorizationRequired Outcome = "authorization_required"
	// Deferred means automatic uploads are holding off until a pause
	// elapses: one the service asked for, or the backoff after an
	// attempt that ran out of time. A forced flush ignores it.
	Deferred Outcome = "deferred"
	// Rejected means the service refused a batch permanently and its
	// records were quarantined; the flush stopped there.
	Rejected Outcome = "rejected"
)

// Result reports what one Flush call did. Batches and Records count
// acknowledged uploads, so a failed flush can still report the progress
// it made before failing. MinClientVersion carries the version the
// service demands when the outcome is UpgradeRequired, so a caller
// never has to read the handshake file across processes for it.
// Outcome is the authoritative signal; an error returned alongside it
// is detail, never a replacement for it.
type Result struct {
	Outcome Outcome
	// Disposition is what this flush did with the last batch it offered,
	// empty when it offered none. Outcome is derived from it, so a
	// caller wanting the flush's answer reads Outcome; a caller checking
	// this client against the upload contract reads this.
	Disposition      Disposition
	Batches          int
	Records          int
	MinClientVersion string
	// UpgradeMessage is what the service said about the refusal, in its
	// own words, empty when it said nothing. Relayed, never parsed.
	UpgradeMessage string
	// AuthorizeURL and AuthorizationMessage carry, for an
	// AuthorizationRequired outcome, where the user completes the
	// authorization and what the service said about it. Either may be
	// empty; the caller has wording of its own for both.
	AuthorizeURL         string
	AuthorizationMessage string
	// SetAside lists the rejections this flush itself wrote for rawcalls
	// that no longer read back as rawcalls. They were never sent; they
	// stop blocking the uploads behind them, and status and doctor keep
	// pointing at them. Each entry carries its Cause, so a renderer says
	// why without re-deriving it.
	SetAside []Rejection
}

// Deps is everything the uploader needs from the world outside it.
type Deps struct {
	Spool *spool.Spool
	// Service posts batches and returns acknowledgements.
	Service *platform.Client
	// DeviceToken reads the device pairing token; empty with no error
	// means the device is signed out and uploads pause.
	DeviceToken func() (string, error)
	// Withdrawn reports whether a project's consent has since been
	// withdrawn, by project id hash. It must answer false when it cannot
	// tell: see dropWithdrawn. Nil means nothing is withdrawn.
	Withdrawn func(projectIDHash string) bool
	Version   string
	// Dir holds the uploader's bookkeeping files.
	Dir string
	// RejectedDir is where the records of a service-rejected batch are
	// moved so they stop blocking the uploads behind them.
	RejectedDir string
	// Run supplies the capture runtime metadata each batch carries. Nil
	// means batches carry zero counters.
	Run  func() batch.Run
	Logf func(format string, args ...any)
	// Now supplies timestamps; nil means time.Now.
	Now func() time.Time
}

// Uploader drains the spool into batches. One Uploader serializes its
// flushes; the proxy process holds the only instance, so there is one
// flusher per machine.
type Uploader struct {
	deps Deps
	mu   sync.Mutex

	// closed refuses every flush after Close ran its last one.
	closed bool

	// Both gates suppress automatic flushes only; a forced flush walks
	// straight past them. They reset on any acknowledged upload and are
	// deliberately not persisted: a fresh process (post-upgrade, or just
	// restarted) gets to find out for itself.
	upgradeRequired bool
	// minClientVersion is what the service last demanded alongside the
	// upgrade gate, and upgradeMessage what it said about the demand;
	// both ride out with the gate's outcome.
	minClientVersion string
	upgradeMessage   string
	// authorization is a gate of its own rather than a second meaning
	// for upgradeRequired: an old build whose account is also
	// unauthorized holds both conditions at once, and sharing one
	// carrier would let status and doctor name only one of them — which
	// is the whole of what this gate buys the user.
	authorization AuthorizationNotice
	notBefore     time.Time
	// timeouts counts consecutive timed-out upload attempts. Each one
	// widens the next attempt's budget and lengthens the pause before
	// it, so the batch at the head of the queue is never retried forever
	// on the terms that just failed. Any acknowledged upload resets it.
	timeouts int
}

// New validates the wiring and builds an uploader.
func New(deps Deps) (*Uploader, error) {
	if deps.Spool == nil || deps.Service == nil || deps.DeviceToken == nil || deps.Dir == "" || deps.RejectedDir == "" {
		return nil, fmt.Errorf("upload: spool, service, device token source, state dir, and rejected dir are required")
	}
	if deps.Run == nil {
		deps.Run = func() batch.Run { return batch.Run{} }
	}
	if deps.Withdrawn == nil {
		deps.Withdrawn = func(string) bool { return false }
	}
	if deps.Logf == nil {
		deps.Logf = func(string, ...any) {}
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Uploader{deps: deps}, nil
}

// ErrClosed reports a flush requested after Close: this uploader has
// done its last work and refuses to start more.
var ErrClosed = errors.New("upload: the uploader has shut down")

// errBudgetSpent reports a flush that stopped because the budget it
// runs on ran out. Nothing is lost by it: records stay in the spool and
// a batch already offered keeps its pending id, so the next flush
// resends it under that id rather than a fresh one.
var errBudgetSpent = errors.New("upload: the flush ran out of its budget; what it did not upload stays in the spool for the next flush")

// Flush uploads what the spool holds. Unforced, it first checks the
// thresholds; forced, it uploads regardless. Either way a flush that
// starts drains the spool to empty, one bounded batch at a time, and
// stops at the first failure with everything unacknowledged still on
// disk for the next attempt.
//
// The result's Outcome stays authoritative when Flush also returns an
// error: the disposition of the batch the flush stopped at decides it,
// and a batch whose disposition asks for nothing but another attempt
// carries no outcome at all — never a leftover in-progress one. Readers
// act on the outcome first.
func (u *Uploader) Flush(force bool) (Result, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return Result{}, ErrClosed
	}
	res, err := u.flush(force, time.Time{})
	if err != nil {
		res.Outcome, res.MinClientVersion, res.UpgradeMessage = "", "", ""
		res.AuthorizeURL, res.AuthorizationMessage = "", ""
		u.reportDisposition(&res)
	}
	return res, err
}

// reportDisposition derives a failed flush's outcome from the
// disposition its last batch reached, reading the gates that
// disposition raised rather than the answer again. A condition
// therefore reports identically on the flush that discovers it and on
// every automatic flush after — the two readings are one reading.
//
// Ack is silent here: a flush that acknowledged a batch and then failed
// at something local has not uploaded successfully, and only a flush
// that runs to the end may say it did.
func (u *Uploader) reportDisposition(res *Result) {
	switch res.Disposition {
	case PauseUploads:
		u.reportUpgradeGate(res)
	case PauseUploadsAuthorize:
		u.reportAuthorizationGate(res)
	case Quarantine:
		res.Outcome = Rejected
	case RetrySameID:
		// Nothing on disk changed, so the only thing left to report is a
		// pause the attempt left behind — which is exactly what the next
		// automatic flush will refuse to start on.
		if u.deps.Now().Before(u.notBefore) {
			res.Outcome = Deferred
		}
	}
}

// reportUpgradeGate and reportAuthorizationGate are the one reading of
// each gate: what it is called and what the service said when it was
// raised. The flush that raises a gate and the flush that is stopped by
// one report through the same pair, so the user is never told two
// different things about one refusal.
func (u *Uploader) reportUpgradeGate(res *Result) {
	res.Outcome = UpgradeRequired
	res.MinClientVersion = u.minClientVersion
	res.UpgradeMessage = u.upgradeMessage
}

func (u *Uploader) reportAuthorizationGate(res *Result) {
	res.Outcome = AuthorizationRequired
	res.AuthorizeURL = u.authorization.URL
	res.AuthorizationMessage = u.authorization.Message
}

// Close runs the uploader's last flush — unforced, the same threshold
// check the periodic cadence makes — within budget, and then refuses
// every later Flush with ErrClosed. It must be called while this
// process still holds whatever excludes a successor flusher (for the
// proxy, its listen port): a flush already running when Close is called
// finishes under the same lock, so once Close returns, no upload
// activity from this process can overlap a successor's.
//
// Because the exclusion is held for exactly as long as this call runs,
// the budget is what the caller is willing to keep a successor waiting.
// It is measured on the wall clock — no injected clock can shorten a
// successor's wait — and what it does not manage to upload stays in the
// spool, with its batch id pinned, for whoever flushes next.
func (u *Uploader) Close(budget time.Duration) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return nil
	}
	u.closed = true
	_, err := u.flush(false, time.Now().Add(budget))
	return err
}

// flush is Flush without the lock and the closed gate. A zero deadline
// is a flush with no budget: it runs until the spool is drained or an
// attempt fails.
func (u *Uploader) flush(force bool, deadline time.Time) (Result, error) {
	res := Result{Outcome: Empty}

	token, err := u.deps.DeviceToken()
	if err != nil {
		return res, fmt.Errorf("upload: reading device token: %w", err)
	}
	if token == "" {
		res.Outcome = Paused
		return res, nil
	}
	if !force {
		if u.upgradeRequired {
			u.reportUpgradeGate(&res)
			return res, nil
		}
		// The upgrade gate is reported first when both are held: a build
		// the service refuses cannot upload however the account stands, so
		// telling the user to go authorize would send them to finish
		// something that changes nothing until they also upgrade.
		if u.authorization.Required {
			u.reportAuthorizationGate(&res)
			return res, nil
		}
		if u.deps.Now().Before(u.notBefore) {
			res.Outcome = Deferred
			return res, nil
		}
	}

	// A pending batch is finished before anything else: its id was
	// already offered to the service, and only its acknowledgement (or
	// the disappearance of its records) releases it.
	if err := u.resendPending(token, &res, deadline); err != nil {
		return res, err
	}

	handshake := LoadHandshake(u.deps.Dir)
	flushBytes := handshake.FlushBytes
	if flushBytes <= 0 {
		flushBytes = DefaultFlushBytes
	}
	flushAge := time.Duration(handshake.FlushAgeSeconds) * time.Second
	if flushAge <= 0 {
		flushAge = DefaultFlushAge
	}

	if res.Batches == 0 && !force {
		usage := u.deps.Spool.Usage()
		if usage == 0 {
			return res, nil
		}
		oldest, ok := u.deps.Spool.Oldest()
		age := time.Duration(0)
		if ok {
			age = u.deps.Now().Sub(oldest)
		}
		if usage < flushBytes && age < flushAge {
			res.Outcome = BelowThreshold
			return res, nil
		}
	}

	for {
		rawcalls, err := u.collect(flushBytes)
		if err != nil {
			return res, fmt.Errorf("upload: reading the spool: %w", err)
		}
		if len(rawcalls) == 0 {
			break
		}
		if !deadline.IsZero() && time.Until(deadline) <= 0 {
			return res, errBudgetSpent
		}
		l, err := openLease(u.deps.Dir, rawcalls)
		if err != nil {
			return res, fmt.Errorf("upload: %w", err)
		}
		if err := u.send(token, l, rawcalls, &res, deadline); err != nil {
			return res, err
		}
	}
	if res.Batches > 0 {
		res.Outcome = Uploaded
	}
	return res, nil
}

// resendPending re-uploads the persisted pending batch from whichever
// of its records still exist. The batch id is reused verbatim: if the
// earlier attempt was ingested and only the acknowledgement was lost,
// the service recognizes the id and does not ingest it again.
func (u *Uploader) resendPending(token string, res *Result, deadline time.Time) error {
	l, ok, err := resumeLease(u.deps.Dir)
	var unreadable *errUnreadablePending
	if errors.As(err, &unreadable) {
		return u.discardUnreadablePending(unreadable.raw)
	}
	if err != nil {
		return fmt.Errorf("upload: reading the pending batch: %w", err)
	}
	if !ok {
		return nil
	}
	wanted := map[string]bool{}
	for _, id := range l.requestIDs() {
		wanted[id] = true
	}
	var rawcalls []spool.Rawcall
	err = u.deps.Spool.Each(func(r spool.Rawcall) error {
		if wanted[r.RequestID] {
			rawcalls = append(rawcalls, r)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("upload: reading the spool: %w", err)
	}
	if len(rawcalls) == 0 {
		// Every record is gone — consent withdrawal deletes spool records
		// out from under a pending batch. Nothing is left to send.
		return l.release()
	}
	return u.send(token, l, rawcalls, res, deadline)
}

// discardUnreadablePending is the recovery for a pending file whose
// bytes stopped parsing: without them the batch id they pinned is lost,
// and keeping the file only fails every flush until someone deletes it
// by hand. Discarding is deliberately narrow — only the positive
// cannot-parse classification reaches here, never a read failure — and
// it is not free: the batch's records are still in the spool and upload
// again under a fresh id, so if the service stored the earlier attempt
// and only its acknowledgement was lost, that batch may be kept twice.
// The bytes are preserved beside the live file, and the trade is stated
// in the log.
func (u *Uploader) discardUnreadablePending(raw []byte) error {
	if err := setAsideUnreadablePending(u.deps.Dir, raw); err != nil {
		return fmt.Errorf("upload: the pending batch record is unreadable and could not be set aside: %w", err)
	}
	u.deps.Logf("upload: warning: the pending batch record was unreadable and was set aside as %s; its rawcalls stay in the spool and upload under a fresh batch id — if the service already stored that batch and only its acknowledgement was lost, it may be stored twice", pendingUnreadableName)
	return nil
}

// send builds, uploads, and settles one batch: on acknowledgement the
// records leave the spool, the lease is settled, and the handshake is
// applied. Failures leave the spool and the lease exactly as they were
// — except records the build itself refused, which move to the rejected
// store so one unreadable file cannot stall every upload behind it.
func (u *Uploader) send(token string, l lease, rawcalls []spool.Rawcall, res *Result, deadline time.Time) error {
	rawcalls, err := u.dropWithdrawn(rawcalls)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	if len(rawcalls) == 0 {
		// Every record of this batch belonged to a project that has since
		// withdrawn; they are deleted, so no later flush can find them and
		// the lease has nothing left to protect.
		return l.release()
	}
	b, refused, err := batch.Build(l.id(), u.deps.Now(), u.deps.Version, rawcalls, u.deps.Run())
	if err != nil {
		u.noteAttempt(err)
		return fmt.Errorf("upload: %w", err)
	}
	if err := u.setAside(l.id(), refused, res); err != nil {
		u.noteAttempt(err)
		return fmt.Errorf("upload: %w", err)
	}
	if len(b.RequestIDs) == 0 {
		// Every record of this batch was set aside. Nothing rides under
		// this id anymore, and the records are out of the spool, so no
		// later flush can re-upload them under a fresh id: the lease has
		// nothing left to protect.
		return l.release()
	}
	if len(refused) > 0 {
		packed := b.RequestIDs
		carries := make(map[string]bool, len(packed))
		for _, rid := range packed {
			carries[rid] = true
		}
		kept := rawcalls[:0:0]
		for _, rc := range rawcalls {
			if carries[rc.RequestID] {
				kept = append(kept, rc)
			}
		}
		rawcalls = kept
	}
	// One attempt's budget normally scales with the batch and with the
	// attempts that timed out before it, up to a cap wide enough for a
	// large batch on a slow link. A flush running on a budget of its own
	// gets whatever is left of that instead: its caller holds something
	// a successor is waiting for, so an attempt that would outlast the
	// budget is cut off there, and its batch — id already pinned — waits
	// in the spool for the next flush.
	budget := platform.UploadBudget(int64(len(b.Envelope))+int64(b.Records.Len()), u.timeouts)
	if !deadline.IsZero() {
		if budget = min(budget, time.Until(deadline)); budget <= 0 {
			return errBudgetSpent
		}
	}
	ack, err := u.deps.Service.UploadBatch(token, b.ID, b.Envelope, b.Records, budget)
	u.noteAttempt(err)
	if err != nil {
		res.Disposition, err = u.settleFailure(l, rawcalls, err)
		return err
	}
	res.Disposition = Ack
	u.upgradeRequired = false
	u.minClientVersion, u.upgradeMessage = "", ""
	u.authorization = AuthorizationNotice{}
	u.notBefore = time.Time{}
	u.timeouts = 0

	if err := l.settle(u.deps.Spool, b.RequestIDs); err != nil {
		return fmt.Errorf("upload: %w", err)
	}

	res.Batches++
	res.Records += len(b.RequestIDs)
	u.applyHandshake(ack.Handshake)
	u.noteUpload(Receipt{
		BatchID: ack.BatchID,
		Records: len(b.RequestIDs),
		Bytes:   int64(len(b.Envelope) + b.Records.Len()),
		At:      u.deps.Now().UTC(),
	})
	return nil
}

// dropWithdrawn takes out of a batch every rawcall whose project has
// since withdrawn consent, and deletes those records from the spool.
//
// Withdrawal is enforced in two processes that cannot order themselves
// against each other: the proxy re-reads the routing table when it
// writes a capture — through a cache with a lifetime of its own — while
// `disable` deletes the project's spooled records from a separate,
// short-lived process. A streamed exchange whose response closes just
// after a revoke is written on a stale verdict, and if disable's scan
// has already swept the day directory that record stays. Nothing looked
// at it again, because the uploader did not consult consent, so it
// shipped: data captured after the user withdrew.
//
// The consent store is the durable record of the decision and outlives
// both processes, so the last door out asks it. It is a gate, never an
// oracle to guess with: a record no project can be read out of, and a
// store that cannot be read, both count as not withdrawn — deleting
// captured data on a failed read is the worse error. 2026-08-25.
func (u *Uploader) dropWithdrawn(rawcalls []spool.Rawcall) ([]spool.Rawcall, error) {
	kept := rawcalls[:0:0]
	withdrawn := map[string]bool{}
	for _, rc := range rawcalls {
		if hash, ok := envelope.ProjectIDHashOf(rc.Data); ok && u.deps.Withdrawn(hash) {
			withdrawn[rc.RequestID] = true
			continue
		}
		kept = append(kept, rc)
	}
	if len(withdrawn) == 0 {
		return kept, nil
	}
	if _, err := u.deps.Spool.DeleteWhere(func(id string) bool { return withdrawn[id] }); err != nil {
		return nil, fmt.Errorf("deleting %d rawcall(s) of a project that withdrew consent: %w", len(withdrawn), err)
	}
	u.deps.Logf("upload: deleted %d rawcall(s) captured for a project that has since withdrawn consent; they were never sent", len(withdrawn))
	return kept, nil
}

// setAside quarantines the rawcalls a build refused — no batch can
// carry them. They are preserved, not deleted: like a service-rejected
// batch they wait for the user, and everything queued behind them
// uploads again.
func (u *Uploader) setAside(batchID string, refused []batch.Refusal, res *Result) error {
	if len(refused) == 0 {
		return nil
	}
	records := make([]spool.Rawcall, len(refused))
	for i, r := range refused {
		records[i] = r.Rawcall
	}
	rej := Rejection{
		BatchID: batchID,
		Records: len(refused),
		Cause:   CauseUnreadable,
		Details: fmt.Sprintf("rawcall %s: %v", refused[0].Rawcall.RequestID, refused[0].Err),
		At:      u.deps.Now().UTC(),
	}
	if err := quarantine(u.deps.RejectedDir, u.deps.Spool, rej, records); err != nil {
		return fmt.Errorf("setting aside %d unreadable rawcall(s): %w", len(refused), err)
	}
	res.SetAside = append(res.SetAside, rej)
	u.deps.Logf("upload: set aside %d unreadable rawcall(s) under %s; they were never sent — run `trajector doctor` to inspect them",
		len(refused), filepath.Join(u.deps.RejectedDir, batchID))
	return nil
}

// settleFailure reads one failed upload as the contract reads it: every
// answer the service can give is one row of the closed Disposition set,
// and this is the single place that maps an answer to its row and
// carries the row out. The row a reader does not find here is
// RetrySameID, which is also the default — for auth failures, timeouts,
// and network errors — and which changes nothing: the spool and the
// pending record stay, and the next flush retries under the same id.
//
// The authorization row stays above the rejection row, the same order
// the answer classes are decided in: a refusal for want of a completed
// data authorization that reached the rejection row would quarantine a
// user's data over a condition they resolve elsewhere, which is the one
// mistake that row exists to prevent.
func (u *Uploader) settleFailure(l lease, rawcalls []spool.Rawcall, err error) (Disposition, error) {
	id := l.id()
	var upgrade *platform.UpgradeRequiredError
	var unauthorized *platform.DataAuthorizationRequiredError
	var limited *platform.RateLimitedError
	var rejected *platform.BatchRejectedError
	var timedOut *platform.UploadTimeoutError
	switch {
	case errors.As(err, &timedOut):
		// Retrying a too-slow upload on the same terms would pin this
		// batch at the queue head until the spool fills. Each consecutive
		// timeout doubles the next attempt's budget and the pause before
		// an automatic flush tries again.
		u.timeouts++
		pause := timeoutBackoff(u.timeouts)
		u.notBefore = u.deps.Now().Add(pause)
		return RetrySameID, fmt.Errorf("upload: batch %s: %w; the next attempt waits %s and allows more time", id, err, pause)
	case errors.As(err, &upgrade):
		// The data is fine; this client is too old. Automatic flushes
		// stop so the batch is not re-uploaded pointlessly every minute.
		u.upgradeRequired = true
		u.minClientVersion = upgrade.MinClientVersion
		u.upgradeMessage = upgrade.Message
		u.noteUpgradeRequired(upgrade.MinClientVersion, upgrade.Message)
		return PauseUploads, fmt.Errorf("upload: batch %s: %w", id, err)
	case errors.As(err, &unauthorized):
		// The data is fine and so is this build; the account has not
		// completed its data authorization. Automatic flushes stop so the
		// batch is not re-offered pointlessly every minute, and nothing is
		// quarantined — the user fixes this on the web, not here.
		u.authorization = AuthorizationNotice{
			Required: true,
			URL:      unauthorized.AuthorizeURL,
			Message:  unauthorized.Message,
		}
		u.noteAuthorizationRequired(unauthorized.AuthorizeURL, unauthorized.Message)
		return PauseUploadsAuthorize, fmt.Errorf("upload: batch %s: %w", id, err)
	case errors.As(err, &limited):
		// RetryAfter arrives already capped at platform.MaxRetryAfter. A
		// rate limit that names no pause still demanded one: without a
		// default, automatic flushes would keep their full cadence
		// against a service that just asked them to slow down.
		pause := limited.RetryAfter
		if pause <= 0 {
			pause = defaultRateLimitPause
			err = fmt.Errorf("%w; automatic flushes wait %s", err, pause)
		}
		u.notBefore = u.deps.Now().Add(pause)
		return RetrySameID, fmt.Errorf("upload: batch %s: %w", id, err)
	case errors.As(err, &rejected):
		// The service says this batch can never be accepted. Move its
		// records aside so the uploads behind it flow again; nothing is
		// deleted, and status/doctor keep pointing at it.
		details := rejected.Status
		if rejected.Details != "" {
			details += ": " + rejected.Details
		}
		rej := Rejection{BatchID: id, Records: len(rawcalls), Cause: CauseRefused, Details: details, At: u.deps.Now().UTC()}
		moved, qerr := l.quarantine(u.deps.RejectedDir, u.deps.Spool, rej, rawcalls)
		if !moved {
			// Nothing moved, so the lease stands: the next flush hits the
			// same rejection and tries the move again under the same id.
			return RetrySameID, fmt.Errorf("upload: batch %s was rejected and could not be set aside: %v (%w)", id, qerr, err)
		}
		if qerr != nil {
			return Quarantine, fmt.Errorf("upload: releasing rejected batch %s: %w", id, qerr)
		}
		u.noteRejection(rej)
		return Quarantine, &errRejected{rej: rej, dir: filepath.Join(u.deps.RejectedDir, id)}
	}
	return RetrySameID, fmt.Errorf("upload: batch %s: %w", id, err)
}

// applyHandshake persists what the service said and puts the spool
// quota into effect at once. A zero field means the service left that
// setting alone, so the acknowledgement merges over the stored
// handshake instead of replacing it. Bookkeeping only — a failure to
// persist costs nothing but the next flush using older settings.
func (u *Uploader) applyHandshake(h platform.Handshake) {
	merged := mergeHandshake(LoadHandshake(u.deps.Dir), h)
	u.deps.Spool.SetQuota(merged.SpoolQuotaBytes)
	if err := saveHandshake(u.deps.Dir, storedHandshake{
		Handshake:  merged,
		ReceivedAt: u.deps.Now().UTC(),
	}); err != nil {
		u.deps.Logf("upload: persisting the service handshake: %v", err)
	}
}

// collect gathers the oldest stored rawcalls up to roughly limit bytes,
// so one batch stays bounded no matter how much a long offline stretch
// accumulated.
func (u *Uploader) collect(limit int64) ([]spool.Rawcall, error) {
	var (
		rawcalls []spool.Rawcall
		total    int64
	)
	errEnough := errors.New("collected enough")
	err := u.deps.Spool.Each(func(r spool.Rawcall) error {
		rawcalls = append(rawcalls, r)
		total += r.Size
		if total >= limit {
			return errEnough
		}
		return nil
	})
	if err != nil && !errors.Is(err, errEnough) {
		return nil, err
	}
	return rawcalls, nil
}

// maxTimeoutBackoff caps the pause between timed-out attempts, so
// backing off can never mute automatic uploads for good.
const maxTimeoutBackoff = 15 * time.Minute

// defaultRateLimitPause is how long automatic flushes hold off after a
// rate limit that names no Retry-After: long enough to actually shed
// load, short enough to resume promptly once the limit lifts. A service
// wanting a different pause names one.
const defaultRateLimitPause = 5 * time.Minute

// timeoutBackoff is how long automatic flushes hold off after the nth
// consecutive timed-out attempt: doubling from a minute, so a
// struggling link is not hammered every flush tick.
func timeoutBackoff(timeouts int) time.Duration {
	pause := time.Minute
	for ; timeouts > 1 && pause < maxTimeoutBackoff; timeouts-- {
		pause *= 2
	}
	return min(pause, maxTimeoutBackoff)
}

func requestIDs(rawcalls []spool.Rawcall) []string {
	ids := make([]string, len(rawcalls))
	for i, r := range rawcalls {
		ids[i] = r.RequestID
	}
	return ids
}

// newBatchID mints the idempotency key for one batch. Unlike a capture,
// an upload can wait: no CSPRNG means no new batch, never a weaker id.
func newBatchID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("minting a batch id: %w", err)
	}
	return "b-" + hex.EncodeToString(b[:]), nil
}
