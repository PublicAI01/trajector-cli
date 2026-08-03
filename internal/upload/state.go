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
const (
	pendingName   = "pending.json"
	stateName     = "state.json"
	handshakeName = "handshake.json"
)

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
// like status to read: the last attempt, the last failure, and the last
// acknowledged upload.
type State struct {
	LastAttemptAt time.Time `json:"last_attempt_at,omitzero"`
	LastError     string    `json:"last_error,omitempty"`
	LastErrorAt   time.Time `json:"last_error_at,omitzero"`
	LastUpload    *Receipt  `json:"last_upload,omitempty"`
}

// storedHandshake is the last service handshake with when it arrived.
type storedHandshake struct {
	platform.Handshake
	ReceivedAt time.Time `json:"received_at,omitzero"`
}

// LoadState reads the uploader's state; a missing or unreadable file is
// an empty state, never an error to act on.
func LoadState(dir string) State {
	var st State
	readJSON(filepath.Join(dir, stateName), &st)
	return st
}

// LoadHandshake reads the last persisted service handshake; missing or
// unreadable means no handshake, and defaults apply.
func LoadHandshake(dir string) platform.Handshake {
	var h storedHandshake
	readJSON(filepath.Join(dir, handshakeName), &h)
	return h.Handshake
}

func saveHandshake(dir string, h storedHandshake) error {
	return writeJSON(filepath.Join(dir, handshakeName), h)
}

func loadPending(dir string) (pending, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, pendingName))
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
		// close.
		return pending{}, false, errors.New("the pending batch record is unreadable")
	}
	return p, true, nil
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

func (u *Uploader) writeState(st State) {
	if err := writeJSON(filepath.Join(u.deps.Dir, stateName), st); err != nil {
		u.deps.Logf("upload: persisting uploader state: %v", err)
	}
}

func readJSON(path string, into any) {
	data, err := os.ReadFile(path)
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
