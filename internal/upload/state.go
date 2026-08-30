package upload

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/platform"
)

// The uploader's bookkeeping files. All of them hold only ids,
// timestamps, and service settings — never record data.
// pendingUnreadableName preserves the bytes of a pending record that
// stopped parsing, set aside so uploads can continue while the
// evidence of what was discarded stays inspectable.
const (
	pendingName           = "pending.json"
	pendingUnreadableName = "pending-unreadable.json"
	stateName             = "state.json"
	handshakeName         = "handshake.json"
)

// BookkeepingFiles names every file the uploader keeps in its state
// directory, for diagnostics to copy verbatim: each holds only ids,
// timestamps, and service settings, never record data.
func BookkeepingFiles() []string {
	return []string{stateName, handshakeName, pendingName, pendingUnreadableName}
}

// pending is a batch that was offered to the service and not yet
// acknowledged. It pins the batch id to its records so a retry reuses
// the id and can never be ingested twice.
type pending struct {
	BatchID    string   `json:"batch_id"`
	RequestIDs []string `json:"request_ids"`
}

// Receipt is the last acknowledged upload.
type Receipt struct {
	BatchID string    `json:"batch_id"`
	Records int       `json:"records"`
	Bytes   int64     `json:"bytes"`
	At      time.Time `json:"at"`
}

// State is what the uploader remembers between flushes for surfaces
// like status to read: the last attempt, the last failure, the last
// acknowledged upload, and the last rejected batch.
type State struct {
	LastAttemptAt time.Time  `json:"last_attempt_at,omitzero"`
	LastError     string     `json:"last_error,omitempty"`
	LastErrorAt   time.Time  `json:"last_error_at,omitzero"`
	LastUpload    *Receipt   `json:"last_upload,omitempty"`
	LastRejected  *Rejection `json:"last_rejected,omitempty"`
}

// storedHandshake is the last service handshake with when it arrived,
// and beside it every "do not upload" instruction this device has been
// given that has not been answered yet — so the file explains the
// uploader's behaviour on its own, to a process that never made the
// upload and to a reader of a diagnostic archive.
//
// Persistence rule, one rule for all of them: the most recent word
// wins, and an acknowledgement is a word. Every field below arrives on
// a refusal rather than on an acknowledgement, and none of them may
// survive one — applyHandshake writes a fresh record whose refusal
// fields are all zero, which is that clearing. That covers the pause a
// 429 or a timed-out attempt left as well: a batch the service has just
// taken is newer evidence than its earlier request to wait, and holding
// uploads back on the older word would keep back exactly what the
// service has demonstrated it will accept.
type storedHandshake struct {
	platform.Handshake
	// UpgradeMessage is what the service said when it last refused this
	// client's version. It sits beside the handshake rather than inside
	// it because it arrives on a 426, not on an acknowledgement.
	UpgradeMessage string `json:"upgrade_message,omitempty"`
	// AuthorizeURL and AuthorizationMessage are what the service last
	// said when it refused this account for want of a completed data
	// authorization. They are kept apart from the version refusal because
	// both refusals can stand at the same time.
	//
	// AuthorizationRequired is stored on its own because the service may
	// supply neither the URL nor the message: without it, a refusal that
	// said nothing would read back as no refusal at all.
	AuthorizationRequired bool   `json:"authorization_required,omitempty"`
	AuthorizeURL          string `json:"authorize_url,omitempty"`
	AuthorizationMessage  string `json:"authorization_message,omitempty"`
	// NotBefore is when automatic uploads may attempt again, and
	// BackoffReason which of the two pauses set it. Kept on disk rather
	// than in the flusher's memory alone so that a surface in another
	// process can say how long the wait has left, and so that a proxy
	// restarted inside the wait honours what is left of it.
	NotBefore     time.Time `json:"not_before,omitzero"`
	BackoffReason Reason    `json:"backoff_reason,omitempty"`
	ReceivedAt    time.Time `json:"received_at,omitzero"`
}

// LoadState reads the uploader's state; a missing or unreadable file is
// an empty state, never an error to act on.
func LoadState(dir string) State {
	var st State
	readJSON(filepath.Join(dir, stateName), &st)
	return st
}

// LoadHandshake reads the last persisted service handshake; missing or
// unreadable means no handshake, and defaults apply. What comes back
// off disk is cleaned again: this file may have been written by a build
// that predates the cleaning, or edited by hand.
func LoadHandshake(dir string) platform.Handshake {
	var h storedHandshake
	readJSON(filepath.Join(dir, handshakeName), &h)
	return h.Handshake.Safe()
}

// LoadStandings reads every reason this device has on disk for not
// uploading, in the order the uploader itself would meet them, for
// status and doctor to report from a process that never made the upload
// itself. A pause that has elapsed by now is not a standing.
//
// All of them are returned, never just the first: both refusal gates
// can stand at the same time — an old build whose account is also
// unauthorized — and a surface that could name only one of them would
// send the user to fix half of what is wrong.
//
// The service's free text is cleaned again on the way out: this file
// may have been written by a build that predates the cleaning, or
// edited by hand.
func LoadStandings(dir, version string, now time.Time) []Standing {
	var h storedHandshake
	readJSON(filepath.Join(dir, handshakeName), &h)

	var standings []Standing
	if s := versionStanding(false,
		platform.SafeServiceText(h.MinClientVersion),
		platform.SafeServiceText(h.UpgradeMessage),
		version,
	); s.Held() {
		standings = append(standings, s)
	}
	if h.AuthorizationRequired {
		standings = append(standings, Standing{
			Reason:       AuthorizationGate,
			AuthorizeURL: platform.SafeServiceURL(h.AuthorizeURL),
			Message:      platform.SafeServiceText(h.AuthorizationMessage),
		})
	}
	if s, ok := h.backoff(now); ok {
		standings = append(standings, s)
	}
	return standings
}

// backoff reports the recorded pause while it still has time to run. An
// unknown reason is carried through rather than dropped: a newer build
// may name a pause this one cannot explain, and the wait itself is
// still the fact that matters.
func (h storedHandshake) backoff(now time.Time) (Standing, bool) {
	if h.BackoffReason == Flowing || !now.Before(h.NotBefore) {
		return Standing{}, false
	}
	return Standing{Reason: h.BackoffReason, NotBefore: h.NotBefore}, true
}

// loadBackoff reads the pause the uploader is holding to. The flusher
// reads it off disk rather than out of its own memory so that a proxy
// restarted inside the wait honours what is left of it: a restart is
// routine here — idle exit, version handover, reboot — and a pause the
// next process ignores puts back exactly the load the service asked to
// shed. The two refusal gates are not read back this way on purpose: a
// 426 is answered by replacing this binary and a 451 off this machine
// entirely, so a fresh process must be allowed to find out for itself.
func loadBackoff(dir string, now time.Time) (Standing, bool) {
	var h storedHandshake
	readJSON(filepath.Join(dir, handshakeName), &h)
	return h.backoff(now)
}

func saveHandshake(dir string, h storedHandshake) error {
	return writeJSON(filepath.Join(dir, handshakeName), h)
}

// mergeHandshake overlays update onto stored under the handshake's
// declared semantics: a zero field means the service left that setting
// alone, so the stored value survives it. Every writer of the handshake
// file merges through here, or the file would mean different things
// depending on who wrote it last.
func mergeHandshake(stored, update platform.Handshake) platform.Handshake {
	if update.MinClientVersion != "" {
		stored.MinClientVersion = update.MinClientVersion
	}
	if update.FlushBytes > 0 {
		stored.FlushBytes = update.FlushBytes
	}
	if update.FlushAgeSeconds > 0 {
		stored.FlushAgeSeconds = update.FlushAgeSeconds
	}
	if update.SpoolQuotaBytes > 0 {
		stored.SpoolQuotaBytes = update.SpoolQuotaBytes
	}
	if update.Notice != "" {
		stored.Notice = update.Notice
	}
	return stored
}

// errUnreadablePending is the positive classification of pending bytes
// that are not a pending record: they parse as no JSON, or they name no
// batch id, so they can never again pin the id they exist to pin. It is
// distinct from a read failure — a file that cannot be read at all may
// still hold a valid record, so only this classification may trigger
// recovery. It carries the judged bytes so the recovery sets aside
// exactly what was judged, not whatever the file holds by then.
type errUnreadablePending struct{ raw []byte }

func (e *errUnreadablePending) Error() string {
	return "the pending batch record is unreadable"
}

func loadPending(dir string) (pending, bool, error) {
	data, err := fsatomic.ReadFile(filepath.Join(dir, pendingName))
	if errors.Is(err, fs.ErrNotExist) {
		return pending{}, false, nil
	}
	if err != nil {
		return pending{}, false, err
	}
	var p pending
	if err := json.Unmarshal(data, &p); err != nil || p.BatchID == "" {
		// An unreadable pending record cannot be resent, but silently
		// dropping it would reopen the double-ingest window it exists to
		// close: the caller owns the recovery and its warning.
		return pending{}, false, &errUnreadablePending{raw: data}
	}
	return p, true, nil
}

// setAsideUnreadablePending replaces the pending file with nothing,
// preserving the unreadable bytes under pendingUnreadableName so what
// was discarded stays inspectable. A later corruption overwrites the
// same file: only the latest discard is evidence worth keeping.
func setAsideUnreadablePending(dir string, raw []byte) error {
	if err := fsatomic.WriteFile(filepath.Join(dir, pendingUnreadableName), raw, 0o600); err != nil {
		return err
	}
	return clearPending(dir)
}

func savePending(dir string, p pending) error {
	return writeJSON(filepath.Join(dir, pendingName), p)
}

func clearPending(dir string) error {
	err := os.Remove(filepath.Join(dir, pendingName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// noteAttempt records when the last upload attempt happened and how it
// went. Best-effort bookkeeping: it must never fail a flush.
func (u *Uploader) noteAttempt(uploadErr error) {
	st := LoadState(u.deps.Dir)
	now := u.deps.Now().UTC()
	st.LastAttemptAt = now
	if uploadErr != nil {
		st.LastError = uploadErr.Error()
		st.LastErrorAt = now
	} else {
		st.LastError = ""
		st.LastErrorAt = time.Time{}
	}
	u.writeState(st)
}

// noteUpload records the last acknowledged upload. Best-effort.
func (u *Uploader) noteUpload(r Receipt) {
	st := LoadState(u.deps.Dir)
	st.LastUpload = &r
	u.writeState(st)
}

// noteRejection records the last rejected batch for status and doctor
// to point at. Best-effort.
func (u *Uploader) noteRejection(rej Rejection) {
	st := LoadState(u.deps.Dir)
	st.LastRejected = &rej
	u.writeState(st)
}

// noteUpgradeRequired keeps the refused-until version and what the
// service said about it where status and doctor read the handshake,
// preserving the rest of it. Best-effort.
func (u *Uploader) noteUpgradeRequired(minVersion, message string) {
	if minVersion == "" && message == "" {
		return
	}
	u.noteRefusal(func(h *storedHandshake) {
		h.Handshake = mergeHandshake(h.Handshake, platform.Handshake{MinClientVersion: minVersion})
		h.UpgradeMessage = message
	}, "the required client version")
}

// noteAuthorizationRequired keeps the refusal and whatever the service
// said about it where status and doctor read the handshake, preserving
// the rest of it. Unlike noteUpgradeRequired it records even when both
// details are empty: the refusal itself is the part surfaces must
// report. Best-effort.
func (u *Uploader) noteAuthorizationRequired(authorizeURL, message string) {
	u.noteRefusal(func(h *storedHandshake) {
		h.AuthorizationRequired = true
		h.AuthorizeURL = authorizeURL
		h.AuthorizationMessage = message
	}, "the data authorization refusal")
}

// noteBackoff keeps the pause a rate limit or a timed-out attempt just
// imposed, and which of the two imposed it, where every surface reads
// what stops uploads. Best-effort, like the rest of this bookkeeping: a
// pause that cannot be written is a pause nobody takes, which costs the
// service another attempt but never costs the user a rawcall.
func (u *Uploader) noteBackoff(reason Reason, notBefore time.Time) {
	u.noteRefusal(func(h *storedHandshake) {
		h.BackoffReason = reason
		h.NotBefore = notBefore
	}, "the upload pause")
}

// noteRefusal records one kind of refusal into the stored handshake
// without disturbing the other. Both kinds can stand at the same time —
// an old build whose account is also unauthorized — so neither writer
// may build its record from scratch, or noting one would silently clear
// the other and leave a surface reporting half of what is wrong.
func (u *Uploader) noteRefusal(apply func(*storedHandshake), what string) {
	var h storedHandshake
	readJSON(filepath.Join(u.deps.Dir, handshakeName), &h)
	apply(&h)
	h.ReceivedAt = u.deps.Now().UTC()
	if err := saveHandshake(u.deps.Dir, h); err != nil {
		u.deps.Logf("upload: persisting %s: %v", what, err)
	}
}

func (u *Uploader) writeState(st State) {
	if err := writeJSON(filepath.Join(u.deps.Dir, stateName), st); err != nil {
		u.deps.Logf("upload: persisting uploader state: %v", err)
	}
}

func readJSON(path string, into any) {
	data, err := fsatomic.ReadFile(path)
	if err != nil {
		return
	}
	// Ignore decode errors: bookkeeping files are advisory and a corrupt
	// one reads as empty.
	_ = json.Unmarshal(data, into)
}

func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return fsatomic.WriteFile(path, append(data, '\n'), 0o600)
}
