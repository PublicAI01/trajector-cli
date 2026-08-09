package upload

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/envelope"
	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/spool"
)

// The rejected store holds rawcalls no upload can carry: records of
// batches the service refused as unacceptable, and records this machine
// set aside itself because they no longer read back as rawcalls. Both
// are moved out of the spool so one bad batch or one unreadable file
// cannot block every upload behind it. The layout is a documented
// product contract:
//
//	<dir>/<batch_id>/<request_id>.json   the rawcall, exactly as spooled
//	<dir>/<batch_id>/reason.json         why the records were set aside
//
// Nothing here is deleted automatically: a quarantined record may still
// correspond to compensation, so it waits — loudly, via status and
// doctor — for a fixed client to requeue it or the user to discard it.
const reasonName = "reason.json"

// noSuchBatch is the one sentence every operation on a named batch uses
// when the store does not hold it.
func noSuchBatch(batchID string) error {
	return fmt.Errorf("no rejected batch %s", batchID)
}

// batchDir resolves one batch id to its directory. The id names a path
// this package reads, moves, and deletes, so an id that is anything but
// a single directory name is refused rather than interpreted: refusing
// beats guessing which tree the caller meant.
func batchDir(rejectedDir, batchID string) (string, error) {
	switch {
	case batchID == "", batchID == ".", batchID == "..":
		return "", noSuchBatch(batchID)
	case batchID != filepath.Base(batchID), strings.ContainsAny(batchID, `/\`):
		return "", noSuchBatch(batchID)
	}
	return filepath.Join(rejectedDir, batchID), nil
}

// Rejection describes one rejected batch, both inside reason.json and
// in the uploader state read by status.
type Rejection struct {
	BatchID string    `json:"batch_id"`
	Records int       `json:"records"`
	Details string    `json:"details,omitempty"`
	At      time.Time `json:"at"`
}

// quarantine moves one batch's records from the spool into the rejected
// store. The copy lands before the spool deletes, so a failure anywhere
// leaves every record present in at least one of the two places;
// rerunning after a partial move overwrites the same files. Writes go
// through fsatomic so a concurrent reader — requeue or withdrawal in
// another process — never observes a half-written record.
func quarantine(rejectedDir string, sp *spool.Spool, rej Rejection, rawcalls []spool.Rawcall) error {
	dir := filepath.Join(rejectedDir, rej.BatchID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, rc := range rawcalls {
		if err := fsatomic.WriteFile(filepath.Join(dir, rc.RequestID+".json"), rc.Data, 0o600); err != nil {
			return err
		}
	}
	reason, err := json.Marshal(rej)
	if err != nil {
		return err
	}
	if err := fsatomic.WriteFile(filepath.Join(dir, reasonName), append(reason, '\n'), 0o600); err != nil {
		return err
	}

	moved := map[string]bool{}
	for _, rc := range rawcalls {
		moved[rc.RequestID] = true
	}
	_, err = sp.DeleteWhere(func(id string) bool { return moved[id] })
	return err
}

// PurgeRejected deletes a project's rawcalls from the rejected store,
// for consent withdrawal: rejected records are still local unuploaded
// data and withdrawal must reach them too. Batch directories left empty
// of records are removed with their reason files.
func PurgeRejected(rejectedDir, projectIDHash string) (int, error) {
	batches, err := os.ReadDir(rejectedDir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, b := range batches {
		if !b.IsDir() {
			continue
		}
		dir := filepath.Join(rejectedDir, b.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			return deleted, err
		}
		remaining := 0
		for _, f := range files {
			name := f.Name()
			if f.IsDir() || name == reasonName || filepath.Ext(name) != ".json" {
				continue
			}
			path := filepath.Join(dir, name)
			data, err := fsatomic.ReadFile(path)
			if err != nil {
				return deleted, err
			}
			hash, ok := envelope.ProjectIDHashOf(data)
			if !ok || hash != projectIDHash {
				remaining++
				continue
			}
			if err := os.Remove(path); err != nil {
				return deleted, err
			}
			deleted++
		}
		if remaining == 0 {
			if err := os.RemoveAll(dir); err != nil {
				return deleted, err
			}
		}
	}
	return deleted, nil
}

// RejectedBatch is one quarantined batch as the rejected store holds
// it: the directory name, how many rawcalls actually sit there, and the
// recorded reason (zero when reason.json is missing or unreadable —
// the records still count).
type RejectedBatch struct {
	BatchID string
	Records int
	Reason  Rejection
}

// ListRejected reports every quarantined batch, ordered by batch id,
// for the surfaces that must warn while any are waiting and for
// requeueing them.
func ListRejected(rejectedDir string) ([]RejectedBatch, error) {
	batches, err := os.ReadDir(rejectedDir)
	if errors.Is(err, fs.ErrNotExist) {
		// Windows also reports a plain file sitting where the directory
		// belongs as not-exist. Only a path with nothing behind it at all
		// is an empty quarantine; anything present but unreadable must
		// surface as an error, never as no quarantine.
		if _, statErr := os.Lstat(rejectedDir); statErr != nil {
			return nil, nil
		}
	}
	if err != nil {
		return nil, err
	}
	var out []RejectedBatch
	for _, b := range batches {
		if !b.IsDir() {
			continue
		}
		rb := RejectedBatch{BatchID: b.Name()}
		files, err := os.ReadDir(filepath.Join(rejectedDir, b.Name()))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".json" {
				continue
			}
			if f.Name() == reasonName {
				readJSON(filepath.Join(rejectedDir, b.Name(), f.Name()), &rb.Reason)
				continue
			}
			rb.Records++
		}
		out = append(out, rb)
	}
	return out, nil
}

// Requeue moves one quarantined batch's records back into the spool so
// the next flush repacks them under a fresh batch id (the rejected id
// was never acknowledged, so no idempotency is at stake). Each record
// is spooled before its quarantined copy is removed, mirroring
// quarantine's crash safety in reverse. A record that no longer parses
// as a rawcall envelope cannot re-enter a spool whose index derives
// from the envelope: it stays quarantined with reason.json while every
// readable record still moves, and the error reports what stayed.
func Requeue(rejectedDir string, sp *spool.Spool, batchID string) (Rejection, int, error) {
	dir, err := batchDir(rejectedDir, batchID)
	if err != nil {
		return Rejection{}, 0, err
	}
	files, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Rejection{}, 0, noSuchBatch(batchID)
	}
	if err != nil {
		return Rejection{}, 0, err
	}
	var rej Rejection
	readJSON(filepath.Join(dir, reasonName), &rej)

	moved := 0
	var stuck []error
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || name == reasonName || filepath.Ext(name) != ".json" {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := fsatomic.ReadFile(path)
		if err != nil {
			stuck = append(stuck, err)
			continue
		}
		env, err := envelope.Parse(data)
		if err != nil {
			stuck = append(stuck, fmt.Errorf("record %s is not a readable rawcall and stays quarantined: %w", name, err))
			continue
		}
		if err := sp.Write(env); err != nil {
			return rej, moved, err
		}
		if err := os.Remove(path); err != nil {
			return rej, moved, err
		}
		moved++
	}
	if len(stuck) > 0 {
		return rej, moved, errors.Join(stuck...)
	}
	return rej, moved, os.RemoveAll(dir)
}

// Discard deletes one quarantined batch and reports the recorded reason
// with how many rawcalls left with it. It is Requeue's dual and the
// terminal half of the pair: a record that no longer parses as an
// envelope can never re-enter the spool, so deletion is the only way it
// leaves the quarantine. Discard therefore reads nothing back and
// judges nothing — every record in the batch counts and the directory
// goes whole.
func Discard(rejectedDir, batchID string) (Rejection, int, error) {
	dir, err := batchDir(rejectedDir, batchID)
	if err != nil {
		return Rejection{}, 0, err
	}
	files, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Rejection{}, 0, noSuchBatch(batchID)
	}
	if err != nil {
		return Rejection{}, 0, err
	}
	var rej Rejection
	readJSON(filepath.Join(dir, reasonName), &rej)

	records := 0
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || name == reasonName || filepath.Ext(name) != ".json" {
			continue
		}
		records++
	}
	if err := os.RemoveAll(dir); err != nil {
		return rej, 0, err
	}
	return rej, records, nil
}

// errRejected is what a flush returns after quarantining a batch: the
// flush stops, but the queue behind the batch is no longer blocked.
type errRejected struct {
	rej Rejection
	dir string
}

func (e *errRejected) Error() string {
	return fmt.Sprintf("the service rejected batch %s; %d rawcall(s) moved to %s and will not be retried automatically (%s)",
		e.rej.BatchID, e.rej.Records, e.dir, e.rej.Details)
}
