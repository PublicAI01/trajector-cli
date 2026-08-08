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
	"github.com/PublicAI01/trajector-cli/internal/platform"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// Default flush thresholds, used until a handshake overrides them.
const (
	DefaultFlushBytes int64 = 10 << 20
	DefaultFlushAge         = 24 * time.Hour
)

// Outcome names the terminal states of one Flush call.
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
	// Deferred means the service asked to slow down and the pause has
	// not elapsed. A forced flush ignores it.
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
	Outcome          Outcome
	Batches          int
	Records          int
	MinClientVersion string
	// Unreadable counts rawcalls this flush moved into the rejected
	// store because they no longer read back as rawcalls. They were
	// never sent; they stop blocking the uploads behind them, and
	// status and doctor keep pointing at them.
	Unreadable int
}

// Deps is everything the uploader needs from the world outside it.
type Deps struct {
	Spool *spool.Spool
	// Service posts batches and returns acknowledgements.
	Service *platform.Client
	// DeviceToken reads the device pairing token; empty with no error
	// means the device is signed out and uploads pause.
	DeviceToken func() (string, error)
	Version     string
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
	// upgrade gate; it rides out with the gate's outcome.
	minClientVersion string
	notBefore        time.Time
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

// Flush uploads what the spool holds. Unforced, it first checks the
// thresholds; forced, it uploads regardless. Either way a flush that
// starts drains the spool to empty, one bounded batch at a time, and
// stops at the first failure with everything unacknowledged still on
// disk for the next attempt.
//
// The result's Outcome stays authoritative when Flush also returns an
// error: a failure of a known class (UpgradeRequired, Deferred,
// Rejected) carries its classifying outcome with the error as detail,
// and a failure of no known class carries no outcome at all — never a
// leftover in-progress one. Readers act on the outcome first.
func (u *Uploader) Flush(force bool) (Result, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return Result{}, ErrClosed
	}
	res, err := u.flush(force)
	if err != nil {
		res.Outcome, res.MinClientVersion = "", ""
		classifyFailure(err, &res)
	}
	return res, err
}

// classifyFailure stamps onto the result the outcome a failure's class
// implies, so the classified arms are reachable on the first encounter
// — not only after an in-process gate remembers the condition.
func classifyFailure(err error, res *Result) {
	var upgrade *platform.UpgradeRequiredError
	var limited *platform.RateLimitedError
	var rejection *errRejected
	switch {
	case errors.As(err, &upgrade):
		res.Outcome = UpgradeRequired
		res.MinClientVersion = upgrade.MinClientVersion
	case errors.As(err, &limited):
		res.Outcome = Deferred
	case errors.As(err, &rejection):
		res.Outcome = Rejected
	}
}

// Close runs the uploader's last flush — unforced, the same threshold
// check the periodic cadence makes — and then refuses every later
// Flush with ErrClosed. It must be called while this process still
// holds whatever excludes a successor flusher (for the proxy, its
// listen port): a flush already running when Close is called finishes
// under the same lock, so once Close returns, no upload activity from
// this process can overlap a successor's.
func (u *Uploader) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return nil
	}
	u.closed = true
	_, err := u.flush(false)
	return err
}

// flush is Flush without the lock and the closed gate.
func (u *Uploader) flush(force bool) (Result, error) {
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
			res.Outcome = UpgradeRequired
			res.MinClientVersion = u.minClientVersion
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
	if err := u.resendPending(token, &res); err != nil {
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
		id, err := newBatchID()
		if err != nil {
			return res, fmt.Errorf("upload: %w", err)
		}
		if err := savePending(u.deps.Dir, pending{BatchID: id, RequestIDs: requestIDs(rawcalls)}); err != nil {
			// Without the pending record a lost acknowledgement could be
			// re-uploaded under a fresh id and ingested twice; better not
			// to start.
			return res, fmt.Errorf("upload: recording the batch before sending it: %w", err)
		}
		if err := u.send(token, id, rawcalls, &res); err != nil {
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
func (u *Uploader) resendPending(token string, res *Result) error {
	p, ok, err := loadPending(u.deps.Dir)
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
	for _, id := range p.RequestIDs {
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
		return clearPending(u.deps.Dir)
	}
	return u.send(token, p.BatchID, rawcalls, res)
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
// records leave the spool, the pending record is released, and the
// handshake is applied. Failures leave the spool and the pending
// record exactly as they were — except records the build itself
// refused, which move to the rejected store so one unreadable file
// cannot stall every upload behind it.
func (u *Uploader) send(token, id string, rawcalls []spool.Rawcall, res *Result) error {
	b, rawcalls, err := u.buildReadable(id, rawcalls, res)
	if err != nil {
		u.noteAttempt(err)
		return fmt.Errorf("upload: %w", err)
	}
	if len(b.RequestIDs) == 0 {
		// Every record of this batch was set aside. Nothing rides under
		// this id anymore, and the records are out of the spool, so no
		// later flush can re-upload them under a fresh id: the pending
		// record has nothing left to protect.
		return clearPending(u.deps.Dir)
	}
	budget := platform.UploadBudget(int64(len(b.Envelope))+int64(b.Records.Len()), u.timeouts)
	ack, err := u.deps.Service.UploadBatch(token, b.ID, b.Envelope, b.Records, budget)
	u.noteAttempt(err)
	if err != nil {
		return u.settleFailure(id, rawcalls, err)
	}
	u.upgradeRequired = false
	u.notBefore = time.Time{}
	u.timeouts = 0

	uploaded := map[string]bool{}
	for _, rid := range b.RequestIDs {
		uploaded[rid] = true
	}
	if _, err := u.deps.Spool.DeleteWhere(func(id string) bool { return uploaded[id] }); err != nil {
		// The batch is acknowledged but its records are still on disk.
		// The pending record must survive so the next flush retries under
		// the same id and the service ignores the duplicate.
		return fmt.Errorf("upload: deleting acknowledged records: %w", err)
	}
	if err := clearPending(u.deps.Dir); err != nil {
		return fmt.Errorf("upload: releasing the pending batch: %w", err)
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

// buildReadable packs one batch from whichever of the rawcalls still
// read back as rawcalls, returning the batch alongside the records it
// actually carries. A record the build refuses moves into the rejected
// store — the same quarantine a service-rejected batch uses, so status
// sees it, doctor lists it, and requeue owns whatever comes next — and
// packing retries with the rest. A zero batch with a nil error means
// no readable record was left.
func (u *Uploader) buildReadable(id string, rawcalls []spool.Rawcall, res *Result) (batch.Batch, []spool.Rawcall, error) {
	var unreadable []spool.Rawcall
	var reasons []error
	for {
		if len(rawcalls) == 0 {
			return batch.Batch{}, nil, u.setAside(id, unreadable, reasons, res)
		}
		b, err := batch.Build(id, u.deps.Now(), u.deps.Version, rawcalls, u.deps.Run())
		var record *batch.RecordError
		if !errors.As(err, &record) {
			if err != nil {
				return batch.Batch{}, rawcalls, err
			}
			return b, rawcalls, u.setAside(id, unreadable, reasons, res)
		}
		kept, refused, found := splitOut(rawcalls, record.RequestID)
		if !found {
			// The refusal names a record this batch does not hold; setting
			// records aside on that answer would guess. Refusing beats
			// guessing, so the whole batch stays put.
			return batch.Batch{}, rawcalls, err
		}
		rawcalls = kept
		unreadable = append(unreadable, refused)
		reasons = append(reasons, record)
	}
}

// setAside quarantines rawcalls no batch can carry. They are preserved,
// not deleted: like a service-rejected batch they wait for the user,
// and everything queued behind them uploads again.
func (u *Uploader) setAside(batchID string, unreadable []spool.Rawcall, reasons []error, res *Result) error {
	if len(unreadable) == 0 {
		return nil
	}
	rej := Rejection{
		BatchID: batchID,
		Records: len(unreadable),
		Details: fmt.Sprintf("never sent: unreadable in the spool (%v)", reasons[0]),
		At:      u.deps.Now().UTC(),
	}
	if err := quarantine(u.deps.RejectedDir, u.deps.Spool, rej, unreadable); err != nil {
		return fmt.Errorf("setting aside %d unreadable rawcall(s): %w", len(unreadable), err)
	}
	res.Unreadable += len(unreadable)
	u.deps.Logf("upload: set aside %d unreadable rawcall(s) under %s; they were never sent — run `trajector doctor` to inspect them",
		len(unreadable), filepath.Join(u.deps.RejectedDir, batchID))
	return nil
}

// splitOut removes the named record. Each build refusal must shrink the
// batch by exactly one record, so the retry loop always terminates.
func splitOut(rawcalls []spool.Rawcall, requestID string) (kept []spool.Rawcall, refused spool.Rawcall, found bool) {
	for i, rc := range rawcalls {
		if rc.RequestID == requestID {
			return append(rawcalls[:i:i], rawcalls[i+1:]...), rc, true
		}
	}
	return rawcalls, spool.Rawcall{}, false
}

// settleFailure turns one failed upload into the behavior its class
// demands. The default — and the only option for auth failures,
// timeouts, and network errors — is to change nothing: the spool and
// the pending record stay, and the next flush retries.
func (u *Uploader) settleFailure(id string, rawcalls []spool.Rawcall, err error) error {
	var upgrade *platform.UpgradeRequiredError
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
		return fmt.Errorf("upload: batch %s: %w; the next attempt waits %s and allows more time", id, err, pause)
	case errors.As(err, &upgrade):
		// The data is fine; this client is too old. Automatic flushes
		// stop so the batch is not re-uploaded pointlessly every minute.
		u.upgradeRequired = true
		u.minClientVersion = upgrade.MinClientVersion
		u.noteUpgradeRequired(upgrade.MinClientVersion)
	case errors.As(err, &limited):
		// RetryAfter arrives already capped at platform.MaxRetryAfter.
		if limited.RetryAfter > 0 {
			u.notBefore = u.deps.Now().Add(limited.RetryAfter)
		}
	case errors.As(err, &rejected):
		// The service says this batch can never be accepted. Move its
		// records aside so the uploads behind it flow again; nothing is
		// deleted, and status/doctor keep pointing at it.
		details := rejected.Status
		if rejected.Details != "" {
			details += ": " + rejected.Details
		}
		rej := Rejection{BatchID: id, Records: len(rawcalls), Details: details, At: u.deps.Now().UTC()}
		if qerr := quarantine(u.deps.RejectedDir, u.deps.Spool, rej, rawcalls); qerr != nil {
			// The batch stays pending: the next flush hits the same
			// rejection and tries the move again.
			return fmt.Errorf("upload: batch %s was rejected and could not be set aside: %v (%w)", id, qerr, err)
		}
		if cerr := clearPending(u.deps.Dir); cerr != nil {
			return fmt.Errorf("upload: releasing rejected batch %s: %w", id, cerr)
		}
		u.noteRejection(rej)
		return &errRejected{rej: rej, dir: filepath.Join(u.deps.RejectedDir, id)}
	}
	return fmt.Errorf("upload: batch %s: %w", id, err)
}

// applyHandshake persists what the service said and puts the spool
// quota into effect at once. Bookkeeping only — a failure to persist
// costs nothing but the next flush using older settings.
func (u *Uploader) applyHandshake(h platform.Handshake) {
	u.deps.Spool.SetQuota(h.SpoolQuotaBytes)
	if err := saveHandshake(u.deps.Dir, storedHandshake{
		Handshake:  h,
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
