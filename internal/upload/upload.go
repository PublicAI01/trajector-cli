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
)

// Result reports what one Flush call did. Batches and Records count
// acknowledged uploads, so a failed flush can still report the progress
// it made before failing.
type Result struct {
	Outcome Outcome
	Batches int
	Records int
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
}

// New validates the wiring and builds an uploader.
func New(deps Deps) (*Uploader, error) {
	if deps.Spool == nil || deps.Service == nil || deps.DeviceToken == nil || deps.Dir == "" {
		return nil, fmt.Errorf("upload: spool, service, device token source, and state dir are required")
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

// Flush uploads what the spool holds. Unforced, it first checks the
// thresholds; forced, it uploads regardless. Either way a flush that
// starts drains the spool to empty, one bounded batch at a time, and
// stops at the first failure with everything unacknowledged still on
// disk for the next attempt.
func (u *Uploader) Flush(force bool) (Result, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	res := Result{Outcome: Empty}

	token, err := u.deps.DeviceToken()
	if err != nil {
		return res, fmt.Errorf("upload: reading device token: %w", err)
	}
	if token == "" {
		res.Outcome = Paused
		return res, nil
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

// send builds, uploads, and settles one batch: on acknowledgement the
// records leave the spool, the pending record is released, and the
// handshake is applied. Failures leave the spool and the pending
// record exactly as they were.
func (u *Uploader) send(token, id string, rawcalls []spool.Rawcall, res *Result) error {
	b, err := batch.Build(id, u.deps.Now(), u.deps.Version, rawcalls, u.deps.Run())
	if err != nil {
		u.noteAttempt(err)
		return fmt.Errorf("upload: %w", err)
	}
	ack, err := u.deps.Service.UploadBatch(token, b.ID, b.Envelope, b.Records)
	u.noteAttempt(err)
	if err != nil {
		return fmt.Errorf("upload: batch %s: %w", id, err)
	}

	uploaded := map[string]bool{}
	for _, rid := range b.RequestIDs {
		uploaded[rid] = true
	}
	if _, err := u.deps.Spool.DeleteWhere(func(r spool.Rawcall) bool { return uploaded[r.RequestID] }); err != nil {
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
		Bytes:   int64(len(b.Envelope) + len(b.Records)),
		At:      u.deps.Now().UTC(),
	})
	return nil
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
