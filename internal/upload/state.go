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

// storedHandshake is the last service handshake with when it arrived.
type storedHandshake struct {
	platform.Handshake
	// UpgradeMessage is what the service said when it last refused this
	// client's version. It sits beside the handshake rather than inside
	// it because it arrives on a 426, not on an acknowledgement — and
	// because it must not survive one: an acknowledged upload is the
	// service accepting this version, which makes the refusal it
	// explained no longer true. applyHandshake writes a fresh record
	// with this field zero, which is that clearing.
	UpgradeMessage string    `json:"upgrade_message,omitempty"`
	ReceivedAt     time.Time `json:"received_at,omitzero"`
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

// LoadUpgradeMessage reads what the service said when it last refused
// this client's version, for status and doctor to relay from a process
// that never made the upload itself. Empty when the service never
// refused, said nothing, or has since acknowledged an upload.
func LoadUpgradeMessage(dir string) string {
	var h storedHandshake
	readJSON(filepath.Join(dir, handshakeName), &h)
	return platform.SafeServiceText(h.UpgradeMessage)
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
	err := os.Remove(filepath.Join(dir, pendingName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
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
	if err := saveHandshake(u.deps.Dir, storedHandshake{
		Handshake:      mergeHandshake(LoadHandshake(u.deps.Dir), platform.Handshake{MinClientVersion: minVersion}),
		UpgradeMessage: message,
		ReceivedAt:     u.deps.Now().UTC(),
	}); err != nil {
		u.deps.Logf("upload: persisting the required client version: %v", err)
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
